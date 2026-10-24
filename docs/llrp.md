# LLRP package

`github.com/iomz/tagstrak/v2/llrp` is the reusable, public part of tagstrak.
It encodes and decodes LLRP messages and parameters with explicit bounds, and does nothing else: dialing, deadlines, retries, logging, and connection ownership stay with the caller.
gosstrak's reader session and golemu's emulator are both built on it.

## API

| Area | Exports |
| --- | --- |
| Frames | `Header`, `Message`, `ReadMessage`, `WriteMessage`, `DecodeMessage`, `EncodeMessage`, `MessageHeaderSize` and the message-type constants |
| Parameters | `Parameter`, `DecodeParameters`, `EncodeParameter`, `EncodeParameters` |
| Tag reports | `DecodeReadEvents`, `ReadEvent`, `NewTagReportDataParam`, `EPCData` |
| Typed messages | `KeepaliveMessage`, `KeepaliveAckMessage`, `ReaderEventNotificationMessage`, `SetReaderConfigMessage`, `SetReaderConfigResponseMessage`, `ROAccessReportMessage` |
| Bounds | `Limits`, `DefaultLimits` |
| Errors | `ErrFrameTooSmall` and the other `Err*` values, matched with `errors.Is` |

Everything exported is covered by the `v2` compatibility promise.
The v1 `go-llrp` builders, such as the capability tables, raw-byte message builders, `Pack`, and `ROAccessReport.Send`, were removed before v2.0.0 because no v2 code used them and they would have frozen an untyped API and a network side effect into the package; they remain at the `v1.0.0` tag.

## Reading reports from a reader

```go
limits := llrp.DefaultLimits()
for {
	m, err := llrp.ReadMessage(conn, limits) // reads one bounded frame
	if err != nil {
		return err
	}
	switch m.Header.Type {
	case llrp.KeepaliveHeader:
		if err := llrp.WriteMessage(conn, llrp.KeepaliveAckMessage(m.Header.ID), limits); err != nil {
			return err
		}
	case llrp.ROAccessReportHeader:
		events, err := llrp.DecodeReadEvents(m.Payload, limits)
		if err != nil {
			return err // malformed report: the caller decides whether to drop the session
		}
		for _, e := range events {
			handle(e.ID, e.PC) // EPC bytes, and the PC word when the reader sent one
		}
	}
}
```

`ReadMessage` never owns the reader; set deadlines on the connection yourself.
The same flow runs, with checked output, as `Example` in `llrp/example_test.go`.

## Bounds and ownership

`Limits` bounds the work done on untrusted input; zero fields select the defaults:

| Field | Default |
| --- | ---: |
| `MaxFrameSize` | 1 MiB |
| `MaxParameterSize` | 32 KiB |
| `MaxParameters` | 4,096 per parameter list |

Frame lengths are validated before anything is allocated.
`Message.Payload` and every `Parameter.Data` returned by `DecodeParameters` are copies the caller owns.
`DecodeReadEvents` validates each parameter list in full before inspecting it, inspects nested parameters in place, and copies each event's EPC and PC once, so events never alias the input.
It accepts the EPC in either EPCData form (TLV 241 or TV 13) and skips tag reports without an EPC.
Unknown TLV parameters are preserved by `DecodeParameters`; unknown TV parameters are rejected, because their length is implicit.

## Verification

```sh
go test -race ./llrp
go test -run '^$' -fuzz '^FuzzDecodeReadEventsMatchesReference$' -fuzztime 2m ./llrp
```

Typed messages are pinned to the bytes the v1 builders produced.
Fuzz targets cover frames, parameter lists, and read events; `FuzzDecodeReadEventsMatchesReference` holds the in-place report decoder to the earlier copying one, events and errors alike.
