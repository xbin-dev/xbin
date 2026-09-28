// Package wsinterop checks the SDK's WebSocket (sdk/ws, standard library
// only) against gorilla/websocket, the library xbind itself speaks: an
// sdk/ws client against a gorilla server and a gorilla client against an
// sdk/ws server — big binary messages, fragments, pings both ways, the
// close handshake and the message limit.
package wsinterop
