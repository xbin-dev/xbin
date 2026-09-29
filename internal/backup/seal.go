package backup

// seal.go — sealed archives (plans/partitions/11-backup-encryption.md §4;
// PD-25, PD-56). Every archive xbind writes while the vault barrier is set
// up is today's tar, encrypted under a backup subkey:
//
//	"XBINSEAL"      8 bytes; a tar xbind writes begins with "backup.json"
//	u32 big-endian  the header's length (at most 4 KiB)
//	header          cleartext JSON {"v":1, "subkey":"bk-…", "salt":"<b64, 32 bytes>",
//	                "chunk":65536, "kind":"main|data|deployment|partition", "created":"…"}
//	body            AES-256-GCM, STREAM: key = HKDF-SHA256(subkey, salt, "xbin/archive/v1");
//	                chunk i's nonce = 11-byte big-endian i ‖ 1 byte (1 on the last chunk);
//	                chunk 0's additional data is the header's bytes
//
// Each chunk holds chunk plaintext bytes (the last one at most that, maybe
// none) and its 16-byte tag, so a chunk changed, moved, dropped or added,
// a header changed, and a stream cut short all fail to open. The subkey's
// id is the only thing about the key an archiver sees.
//
// Open reads either kind: a sealed archive is decrypted with the subkey its
// header names, anything else is read as the plaintext tar every older
// xbind wrote, exactly as NewReader reads it.

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"golang.org/x/crypto/hkdf"
)

// SealMagic starts every sealed archive.
const SealMagic = "XBINSEAL"

const (
	sealVersion   = 1
	SealChunk     = 64 << 10 // plaintext bytes per chunk
	maxSealChunk  = 16 << 20 // the largest chunk a reader accepts
	maxSealHeader = 4 << 10
	sealInfo      = "xbin/archive/v1"
	sealSaltLen   = 32
	sealKeyLen    = 32
	sealTagLen    = 16
)

// The kinds of archive a sealed header names.
const (
	KindMain       = "main"
	KindData       = "data"
	KindDeployment = "deployment"
	KindPartition  = "partition"
)

// SubkeyID is a backup subkey's id: its name, the one thing about it an
// archiver sees.
var SubkeyID = regexp.MustCompile(`^bk-[0-9a-f]{32}$`)

// SealHeader is a sealed archive's cleartext header.
type SealHeader struct {
	V       int    `json:"v"`
	Subkey  string `json:"subkey"`
	Salt    []byte `json:"salt"` // base64 on the wire
	Chunk   int    `json:"chunk"`
	Kind    string `json:"kind"`
	Created string `json:"created"`
}

// KeyFunc resolves a subkey id to its key. Its error is Open's, unchanged:
// the caller says why a key is gone (erased, another workspace's).
type KeyFunc func(id string) ([]byte, error)

var (
	// ErrNoKeys is Open's refusal of a sealed archive when it was given no
	// way to find keys.
	ErrNoKeys = errors.New("backup: the archive is sealed and this reader has no backup keys")
	// ErrTampered is a sealed archive whose header or body doesn't
	// authenticate: changed, reordered, cut short or extended.
	ErrTampered = errors.New("backup: the sealed archive is damaged or was tampered with (it doesn't authenticate)")
)

// IsSealed reports whether prefix, an archive's first bytes, starts a sealed
// archive.
func IsSealed(prefix []byte) bool { return bytes.HasPrefix(prefix, []byte(SealMagic)) }

// sealedWriter is NewSealedWriter's stream.
type sealedWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	chunk  int
	buf    []byte // plaintext of the chunk being filled
	out    []byte // a sealed chunk
	i      uint64
	err    error
	closed bool
}

// NewSealedWriter writes a sealed archive of kind under subkey, whose id is
// id, into w: the magic and header at once, then the body as it is written.
// Close seals the last chunk (w stays open). subkey is used only here; the
// caller zeroes it afterwards.
func NewSealedWriter(w io.Writer, subkey []byte, id, kind string) (io.WriteCloser, error) {
	if len(subkey) != sealKeyLen {
		return nil, fmt.Errorf("backup: a subkey is %d bytes, not %d", sealKeyLen, len(subkey))
	}
	if !SubkeyID.MatchString(id) {
		return nil, fmt.Errorf("backup: %q is not a subkey id", id)
	}
	h := SealHeader{V: sealVersion, Subkey: id, Salt: make([]byte, sealSaltLen), Chunk: SealChunk, Kind: kind,
		Created: time.Now().UTC().Format(time.RFC3339)}
	if _, err := rand.Read(h.Salt); err != nil {
		return nil, err
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	aead, err := archiveAEAD(subkey, h.Salt)
	if err != nil {
		return nil, err
	}
	pre := make([]byte, 0, len(SealMagic)+4+len(hb))
	pre = append(pre, SealMagic...)
	pre = binary.BigEndian.AppendUint32(pre, uint32(len(hb)))
	pre = append(pre, hb...)
	if _, err := w.Write(pre); err != nil {
		return nil, err
	}
	return &sealedWriter{w: w, aead: aead, header: hb, chunk: SealChunk, buf: make([]byte, 0, SealChunk),
		out: make([]byte, 0, SealChunk+sealTagLen)}, nil
}

// Write buffers p, sealing each full chunk once more follows it: the last
// chunk is sealed by Close, so it is marked last even when it is full.
func (s *sealedWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.closed {
		return 0, errors.New("backup: write to a closed sealed archive")
	}
	n := 0
	for len(p) > 0 {
		if len(s.buf) == s.chunk {
			if s.err = s.seal(false); s.err != nil {
				return n, s.err
			}
		}
		c := copy(s.buf[len(s.buf):s.chunk], p)
		s.buf = s.buf[:len(s.buf)+c]
		p, n = p[c:], n+c
	}
	return n, nil
}

// Close seals the last chunk.
func (s *sealedWriter) Close() error {
	if s.err != nil || s.closed {
		return s.err
	}
	s.closed = true
	s.err = s.seal(true)
	return s.err
}

func (s *sealedWriter) seal(last bool) error {
	var aad []byte
	if s.i == 0 {
		aad = s.header
	}
	s.out = s.aead.Seal(s.out[:0], chunkNonce(s.i, last), s.buf, aad)
	s.i++
	s.buf = s.buf[:0]
	_, err := s.w.Write(s.out)
	return err
}

// chunkNonce is chunk i's nonce: i in 11 big-endian bytes, then the last
// flag.
func chunkNonce(i uint64, last bool) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[3:11], i)
	if last {
		n[11] = 1
	}
	return n[:]
}

// archiveAEAD is the archive key's AES-256-GCM: HKDF-SHA256 of the subkey
// with the archive's salt.
func archiveAEAD(subkey, salt []byte) (cipher.AEAD, error) {
	key := make([]byte, sealKeyLen)
	if _, err := io.ReadFull(hkdf.New(sha256.New, subkey, salt, []byte(sealInfo)), key); err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open reads an archive: a sealed one through keys, anything else as the
// plaintext tar NewReader reads. Its errors are NewReader's, keys', or
// ErrTampered for a sealed archive that doesn't authenticate — which a
// sealed body's Next may also answer mid-way, at the chunk that fails.
func Open(r io.Reader, keys KeyFunc) (*Reader, error) {
	body, _, err := OpenSealed(r, keys)
	if err != nil {
		return nil, err
	}
	return NewReader(body)
}

// OpenSealed answers a sealed archive's plaintext stream and its header, or
// — for anything else — the archive itself, as it is, and a zero header.
func OpenSealed(r io.Reader, keys KeyFunc) (io.Reader, SealHeader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(len(SealMagic))
	if err != nil || !IsSealed(head) {
		return br, SealHeader{}, nil // plaintext (or too short for either: NewReader says so)
	}
	h, hb, err := readSealHeader(br)
	if err != nil {
		return nil, h, err
	}
	if keys == nil {
		return nil, h, ErrNoKeys
	}
	key, err := keys(h.Subkey)
	if err != nil {
		return nil, h, err
	}
	aead, err := archiveAEAD(key, h.Salt)
	clear(key)
	if err != nil {
		return nil, h, err
	}
	return &openReader{r: br, aead: aead, header: hb, chunk: h.Chunk, ct: make([]byte, h.Chunk+sealTagLen)}, h, nil
}

// ReadSealHeader reads a sealed archive's header from r (at its start);
// false for an archive that isn't sealed.
func ReadSealHeader(r io.Reader) (SealHeader, bool, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(len(SealMagic))
	if err != nil || !IsSealed(head) {
		return SealHeader{}, false, nil
	}
	h, _, err := readSealHeader(br)
	return h, true, err
}

func readSealHeader(br *bufio.Reader) (SealHeader, []byte, error) {
	var h SealHeader
	var pre [len(SealMagic) + 4]byte
	if _, err := io.ReadFull(br, pre[:]); err != nil {
		return h, nil, ErrTampered
	}
	n := binary.BigEndian.Uint32(pre[len(SealMagic):])
	if n == 0 || n > maxSealHeader {
		return h, nil, fmt.Errorf("backup: a sealed archive's header of %d bytes (at most %d): %w", n, maxSealHeader, ErrTampered)
	}
	hb := make([]byte, n)
	if _, err := io.ReadFull(br, hb); err != nil {
		return h, nil, ErrTampered
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return h, nil, fmt.Errorf("backup: a sealed archive's header: %v: %w", err, ErrTampered)
	}
	switch {
	case h.V != sealVersion:
		return h, nil, fmt.Errorf("backup: a sealed archive of format %d, newer than this xbin — upgrade to restore", h.V)
	case !SubkeyID.MatchString(h.Subkey), len(h.Salt) != sealSaltLen, h.Chunk < 1 || h.Chunk > maxSealChunk:
		return h, nil, fmt.Errorf("backup: a sealed archive's header doesn't hold a subkey, salt and chunk size: %w", ErrTampered)
	}
	return h, hb, nil
}

// openReader decrypts a sealed body chunk by chunk. A chunk is the last
// when nothing follows it; one that doesn't open as what its place says
// fails the stream.
type openReader struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	header []byte
	chunk  int
	ct     []byte // the chunk being read, then (in place) its plaintext
	pt     []byte // plaintext not yet returned
	i      uint64
	done   bool
	err    error
}

func (o *openReader) Read(p []byte) (int, error) {
	for len(o.pt) == 0 {
		switch {
		case o.err != nil:
			return 0, o.err
		case o.done:
			return 0, io.EOF
		}
		o.err = o.next()
	}
	n := copy(p, o.pt)
	o.pt = o.pt[n:]
	return n, nil
}

func (o *openReader) next() error {
	n, err := io.ReadFull(o.r, o.ct)
	last := false
	switch {
	case err == io.EOF:
		return ErrTampered // the last chunk is missing: cut short
	case err == io.ErrUnexpectedEOF:
		last = true
	case err != nil:
		return err
	default:
		if _, err := o.r.Peek(1); err == io.EOF {
			last = true
		} else if err != nil {
			return err
		}
	}
	if n < sealTagLen {
		return ErrTampered
	}
	var aad []byte
	if o.i == 0 {
		aad = o.header
	}
	pt, err := o.aead.Open(o.ct[:0], chunkNonce(o.i, last), o.ct[:n], aad)
	if err != nil {
		return ErrTampered
	}
	o.i++
	o.pt, o.done = pt, last
	return nil
}
