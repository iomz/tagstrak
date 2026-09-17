package emulator

import (
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

// writeCycle holds only one bounded report payload at a time. Empty inventory
// produces an empty report, making empty cycles visible and reproducible.
func writeCycle(tags []inventory.Tag, maxFrame uint32, emit func([]byte) error) error {
	body := make([]byte, 0, int(maxFrame)-llrp.MessageHeaderSize)
	parameters := 0
	for _, tag := range tags {
		epc := tag.EPC()
		p, err := llrp.NewTagReportDataParam(epc, uint16(len(epc)/2)<<11)
		if err != nil {
			return err
		}
		if llrp.MessageHeaderSize+len(p) > int(maxFrame) {
			return fmt.Errorf("%w: tag exceeds frame budget", ErrConfig)
		}
		if llrp.MessageHeaderSize+len(body)+len(p) > int(maxFrame) || parameters == llrp.DefaultLimits().MaxParameters {
			if err := emit(body); err != nil {
				return err
			}
			body = body[:0]
			parameters = 0
		}
		body = append(body, p...)
		parameters++
	}
	return emit(body)
}
