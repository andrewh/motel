// Recording export: serialise reconstructed trace trees to a replay sidecar.
package traceimport

import (
	"io"

	"github.com/andrewh/motel/pkg/synth"
)

// WriteRecording streams trace trees to w as a newline-delimited replay
// recording. Each trace becomes one line, so large captures are written
// incrementally without buffering the whole recording in memory.
func WriteRecording(trees []*TraceTree, w io.Writer) error {
	rw := synth.NewRecordingWriter(w)
	for _, tree := range trees {
		rec := synth.RecordedTrace{
			TraceID: tree.TraceID,
			Spans:   make([]synth.RecordedSpan, 0, len(tree.AllNodes)),
		}
		for _, node := range tree.AllNodes {
			s := node.Span
			rec.Spans = append(rec.Spans, synth.RecordedSpan{
				SpanID:     s.SpanID,
				ParentID:   s.ParentID,
				Service:    s.Service,
				Operation:  s.Operation,
				Start:      s.StartTime,
				End:        s.EndTime,
				Error:      s.IsError,
				Attributes: s.Attributes,
			})
		}
		if err := rw.Write(rec); err != nil {
			return err
		}
	}
	return nil
}
