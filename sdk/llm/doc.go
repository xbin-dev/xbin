// Package llm is the OpenAI-compatible wire as xbin's components speak it:
// the request, response and stream types of Chat Completions and of the
// Responses API as far as a proxy and a client need them, token usage read
// from a response body or from an SSE stream as it passes, the model list,
// and a price table that turns usage into cost.
//
// It is shared so the wire is written once: xbind's built-in LLM connector
// (docs/llm.md) meters with it, and a tile that calls or proxies a model
// can use the same types. Standard library only, like the rest of the SDK.
//
// Two wires:
//
//   - Chat Completions (POST /v1/chat/completions): ChatRequest,
//     ChatResponse, ChatChunk, ChatUsage. A stream reports usage in its
//     last chunk only when the request asked for it
//     (stream_options.include_usage; ChatRequest.WithStreamUsage).
//   - Responses (POST /v1/responses): ResponsesRequest, Response,
//     ResponsesEvent, ResponsesUsage. A stream reports usage in its
//     response.completed (or response.incomplete) event.
//
// Usage is the two wires' usage in one shape. UsageFromBody reads it from a
// non-streaming body of either wire; StreamUsage is an io.Writer a relay
// feeds the stream's bytes to (and BodyUsage the same for a plain body), so
// a proxy meters without holding a stream back. ReadSSE is the reading side
// for a client.
package llm
