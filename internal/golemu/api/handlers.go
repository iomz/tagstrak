package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/iomz/tagstrak/v2/internal/inventory"
)

type tagRecord struct {
	EPC string `json:"epc" binding:"required"`
}

// Handler provides inventory HTTP operations without protocol wire types.
type Handler struct{ inventory *inventory.Service }

func NewHandler(inventory *inventory.Service) *Handler { return &Handler{inventory: inventory} }

func (h *Handler) PostTag(c *gin.Context) {
	var records []tagRecord
	if err := c.ShouldBindJSON(&records); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "details": err.Error()})
		return
	}
	inserted, updated := 0, 0
	for index, record := range records {
		tag, err := inventory.ParseTag(record.EPC)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("tag[%d]: %v", index, err)})
			return
		}
		isNew, err := h.inventory.Upsert(tag)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "inventory update failed"})
			return
		}
		if isNew {
			inserted++
		} else {
			updated++
		}
	}
	c.JSON(http.StatusOK, gin.H{"inserted": inserted, "updated": updated})
}

func (h *Handler) DeleteTag(c *gin.Context) {
	var records []tagRecord
	if err := c.ShouldBindJSON(&records); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "details": err.Error()})
		return
	}
	for index, record := range records {
		tag, err := inventory.ParseTag(record.EPC)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("tag[%d]: %v", index, err)})
			return
		}
		deleted, err := h.inventory.Delete(tag)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "inventory update failed"})
			return
		}
		if !deleted {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("tag[%d] not found", index)})
			return
		}
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) GetTags(c *gin.Context) {
	tags := h.inventory.Snapshot()
	records := make([]tagRecord, len(tags))
	for i, tag := range tags {
		records[i] = tagRecord{EPC: tag.Hex()}
	}
	c.JSON(http.StatusOK, records)
}
