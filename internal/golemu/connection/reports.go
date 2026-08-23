package connection

import (
	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func buildTagReportDataStack(tags []inventory.Tag, pdu int) llrp.TagReportDataStack {
	var reports llrp.TagReportDataStack
	for _, tag := range tags {
		epc := tag.EPC()
		parameter := llrp.NewTagReportDataParam(epc, uint16(len(epc)/2)<<11)
		if len(reports) == 0 || 10+len(reports[len(reports)-1].Data)+4+len(parameter) >= pdu {
			reports = append(reports, &llrp.TagReportData{Data: parameter, TagCount: 1})
			continue
		}
		reports[len(reports)-1].Data = append(reports[len(reports)-1].Data, parameter...)
		reports[len(reports)-1].TagCount++
	}
	return reports
}
