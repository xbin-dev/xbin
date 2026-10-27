//! The sidecar's end of xbind's connection factory: the SOCK_SEQPACKET socket
//! xbind hands over as `--fd N` (internal/sandbox/factory_linux.go). Each
//! stream travels as one message: the byte 'c' with one end of a fresh
//! SOCK_STREAM socketpair attached by SCM_RIGHTS. xbind receives it with
//! `Factory.Accept` (MSG_CMSG_CLOEXEC); there is no path anyone else could
//! dial. xbind never writes to the factory, so readability means its end is
//! closed: xbind is gone, and so the sidecar goes too.

use std::io;
use std::mem;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::ptr;

use tokio::io::unix::AsyncFd;

pub struct Factory {
    fd: AsyncFd<OwnedFd>,
}

impl Factory {
    /// Takes ownership of an inherited fd: it becomes close-on-exec and
    /// non-blocking.
    ///
    /// # Safety
    /// fd must be an open socket that nothing else owns.
    pub unsafe fn from_raw(fd: RawFd) -> io::Result<Self> {
        if libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC) < 0 {
            return Err(io::Error::last_os_error());
        }
        let fl = libc::fcntl(fd, libc::F_GETFL);
        if fl < 0 || libc::fcntl(fd, libc::F_SETFL, fl | libc::O_NONBLOCK) < 0 {
            return Err(io::Error::last_os_error());
        }
        let mut ty: libc::c_int = 0;
        let mut len = mem::size_of::<libc::c_int>() as libc::socklen_t;
        if libc::getsockopt(
            fd,
            libc::SOL_SOCKET,
            libc::SO_TYPE,
            &mut ty as *mut _ as *mut libc::c_void,
            &mut len,
        ) < 0
        {
            return Err(io::Error::last_os_error());
        }
        if ty != libc::SOCK_SEQPACKET {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("--fd {fd} is not a SOCK_SEQPACKET socket"),
            ));
        }
        // register: the fd is ours alone from here (the caller's promise)
        let fd = AsyncFd::register(OwnedFd::from_raw_fd(fd))
            .map_err(|e| io::Error::other(e.to_string()))?;
        Ok(Factory { fd })
    }

    /// Sends one connection: the fd travels, the caller still owns (and
    /// should then close) its copy. Waits while xbind's queue is full.
    pub async fn send(&self, fd: RawFd) -> io::Result<()> {
        loop {
            let mut guard = self.fd.writable().await?;
            match guard.try_io(|inner| send_fd(inner.get_ref().as_raw_fd(), fd)) {
                Ok(res) => return res,
                Err(_would_block) => continue,
            }
        }
    }

    /// Resolves once xbind's end is closed.
    pub async fn closed(&self) {
        let mut buf = [0u8; 16];
        loop {
            let Ok(mut guard) = self.fd.readable().await else {
                return;
            };
            match guard.try_io(|inner| {
                let n = unsafe {
                    libc::recv(
                        inner.get_ref().as_raw_fd(),
                        buf.as_mut_ptr() as *mut libc::c_void,
                        buf.len(),
                        libc::MSG_DONTWAIT,
                    )
                };
                if n < 0 {
                    Err(io::Error::last_os_error())
                } else {
                    Ok(n)
                }
            }) {
                Ok(Ok(0)) | Ok(Err(_)) => return,
                Ok(Ok(_)) => continue, // xbind never writes; ignore it
                Err(_would_block) => continue,
            }
        }
    }
}

/// One sendmsg: the byte 'c' and fd as SCM_RIGHTS, never blocking.
pub fn send_fd(sock: RawFd, fd: RawFd) -> io::Result<()> {
    let byte = [b'c'];
    let mut iov = libc::iovec {
        iov_base: byte.as_ptr() as *mut libc::c_void,
        iov_len: 1,
    };
    let space = unsafe { libc::CMSG_SPACE(mem::size_of::<RawFd>() as u32) } as usize;
    let mut cbuf = vec![0u64; space.div_ceil(8)]; // u64: cmsghdr alignment
    let mut msg: libc::msghdr = unsafe { mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;
    msg.msg_control = cbuf.as_mut_ptr() as *mut libc::c_void;
    msg.msg_controllen = space as _;
    unsafe {
        let cmsg = libc::CMSG_FIRSTHDR(&msg);
        (*cmsg).cmsg_level = libc::SOL_SOCKET;
        (*cmsg).cmsg_type = libc::SCM_RIGHTS;
        (*cmsg).cmsg_len = libc::CMSG_LEN(mem::size_of::<RawFd>() as u32) as _;
        ptr::write_unaligned(libc::CMSG_DATA(cmsg) as *mut RawFd, fd);
    }
    loop {
        let n = unsafe { libc::sendmsg(sock, &msg, libc::MSG_DONTWAIT | libc::MSG_NOSIGNAL) };
        if n >= 0 {
            return Ok(());
        }
        let err = io::Error::last_os_error();
        if err.kind() != io::ErrorKind::Interrupted {
            return Err(err);
        }
    }
}

/// xbind's side, for tests: one message and the fd it carries
/// (close-on-exec), or None at EOF.
#[cfg(test)]
pub fn recv_fd(sock: RawFd) -> io::Result<Option<OwnedFd>> {
    let mut byte = [0u8; 1];
    let mut iov = libc::iovec {
        iov_base: byte.as_mut_ptr() as *mut libc::c_void,
        iov_len: 1,
    };
    let space = unsafe { libc::CMSG_SPACE((4 * mem::size_of::<RawFd>()) as u32) } as usize;
    let mut cbuf = vec![0u64; space.div_ceil(8)];
    let mut msg: libc::msghdr = unsafe { mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;
    msg.msg_control = cbuf.as_mut_ptr() as *mut libc::c_void;
    msg.msg_controllen = space as _;
    let n = unsafe { libc::recvmsg(sock, &mut msg, libc::MSG_CMSG_CLOEXEC) };
    if n < 0 {
        return Err(io::Error::last_os_error());
    }
    if n == 0 {
        return Ok(None);
    }
    unsafe {
        let cmsg = libc::CMSG_FIRSTHDR(&msg);
        if cmsg.is_null() || (*cmsg).cmsg_type != libc::SCM_RIGHTS {
            return Err(io::Error::other("no fd in the message"));
        }
        let fd = ptr::read_unaligned(libc::CMSG_DATA(cmsg) as *const RawFd);
        Ok(Some(OwnedFd::from_raw_fd(fd)))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::os::unix::net::UnixStream as StdUnixStream;

    fn seqpacket() -> (RawFd, RawFd) {
        let mut fds = [0; 2];
        let r = unsafe {
            libc::socketpair(
                libc::AF_UNIX,
                libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                0,
                fds.as_mut_ptr(),
            )
        };
        assert_eq!(r, 0);
        (fds[0], fds[1])
    }

    #[tokio::test]
    async fn fd_pass_round_trip() {
        let (xbind, child) = seqpacket();
        let f = unsafe { Factory::from_raw(child) }.unwrap();
        let (mut mine, theirs) = StdUnixStream::pair().unwrap();
        f.send(theirs.as_raw_fd()).await.unwrap();
        drop(theirs);
        let got = recv_fd(xbind).unwrap().expect("a connection");
        let flags = unsafe { libc::fcntl(got.as_raw_fd(), libc::F_GETFD) };
        assert!(flags & libc::FD_CLOEXEC != 0, "received close-on-exec");
        let mut xs = StdUnixStream::from(got);
        mine.write_all(b"ping").unwrap();
        let mut buf = [0u8; 4];
        xs.read_exact(&mut buf).unwrap();
        assert_eq!(&buf, b"ping");
        xs.write_all(b"pong").unwrap();
        mine.read_exact(&mut buf).unwrap();
        assert_eq!(&buf, b"pong");
        drop(xs);
        let mut rest = Vec::new();
        mine.read_to_end(&mut rest).unwrap();
        assert!(rest.is_empty(), "xbind's close reaches the sidecar's end");

        // xbind's end closing resolves closed(), and send fails
        unsafe { libc::close(xbind) };
        tokio::time::timeout(std::time::Duration::from_secs(5), f.closed())
            .await
            .expect("closed() resolves");
        let (_a, b) = StdUnixStream::pair().unwrap();
        assert!(f.send(b.as_raw_fd()).await.is_err());
    }

    #[test]
    fn refuses_a_stream_socket() {
        let (a, _b) = StdUnixStream::pair().unwrap();
        let fd = std::os::fd::IntoRawFd::into_raw_fd(a);
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        let _g = rt.enter();
        assert!(unsafe { Factory::from_raw(fd) }.is_err());
        unsafe { libc::close(fd) };
    }
}
