package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"xiaozhi/manager/backend/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type MeetingController struct {
	DB *gorm.DB
}

type meetingUpsertRequest struct {
	DeviceID      string                  `json:"device_id"`
	MeetingID     string                  `json:"meeting_id"`
	Status        string                  `json:"status"`
	AudioPath     string                  `json:"audio_path"`
	SampleRate    int                     `json:"sample_rate"`
	Channels      int                     `json:"channels"`
	DurationMs    int64                   `json:"duration_ms"`
	Frames        uint64                  `json:"frames"`
	MissingFrames uint64                  `json:"missing_frames"`
	Transcript    string                  `json:"transcript"`
	Summary       string                  `json:"summary"`
	SpeakerCount  int                     `json:"speaker_count"`
	Error         string                  `json:"error"`
	StartedAt     time.Time               `json:"started_at"`
	EndedAt       *time.Time              `json:"ended_at"`
	Segments      []models.MeetingSegment `json:"segments"`
}

func (c *MeetingController) UpsertInternal(ctx *gin.Context) {
	var req meetingUpsertRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "会议数据格式错误"})
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.MeetingID = strings.TrimSpace(req.MeetingID)
	if req.DeviceID == "" || req.MeetingID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "device_id 和 meeting_id 必填"})
		return
	}

	var device models.Device
	if err := c.DB.Where("device_name = ?", req.DeviceID).First(&device).Error; err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "设备尚未绑定到用户"})
		return
	}

	var meeting models.Meeting
	err := c.DB.Where("device_id = ? AND meeting_id = ?", req.DeviceID, req.MeetingID).First(&meeting).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "查询会议失败"})
		return
	}
	if err == gorm.ErrRecordNotFound {
		meeting = models.Meeting{
			UserID:    device.UserID,
			DeviceID:  req.DeviceID,
			MeetingID: req.MeetingID,
			StartedAt: req.StartedAt,
		}
	}
	meeting.Status = req.Status
	meeting.AudioPath = req.AudioPath
	meeting.SampleRate = req.SampleRate
	meeting.Channels = req.Channels
	meeting.DurationMs = req.DurationMs
	meeting.Frames = req.Frames
	meeting.MissingFrames = req.MissingFrames
	meeting.Transcript = req.Transcript
	meeting.Summary = req.Summary
	meeting.SpeakerCount = req.SpeakerCount
	meeting.Error = req.Error
	meeting.EndedAt = req.EndedAt
	if meeting.StartedAt.IsZero() {
		meeting.StartedAt = time.Now().UTC()
	}

	if err := c.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&meeting).Error; err != nil {
			return err
		}
		if len(req.Segments) == 0 {
			return nil
		}
		if err := tx.Where("meeting_ref_id = ?", meeting.ID).Delete(&models.MeetingSegment{}).Error; err != nil {
			return err
		}
		for i := range req.Segments {
			req.Segments[i].ID = 0
			req.Segments[i].MeetingRefID = meeting.ID
		}
		return tx.Create(&req.Segments).Error
	}); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "保存会议失败"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"id": meeting.ID})
}

func (c *MeetingController) List(ctx *gin.Context) {
	userID, ok := ctx.Get("user_id")
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "未授权"})
		return
	}
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query := c.DB.Model(&models.Meeting{}).Where("user_id = ?", userID)
	if keyword := strings.TrimSpace(ctx.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where(
			"(meeting_id LIKE ? OR device_id LIKE ? OR transcript LIKE ? OR summary LIKE ?)",
			like, like, like, like,
		)
	}
	if deviceID := strings.TrimSpace(ctx.Query("device_id")); deviceID != "" {
		query = query.Where("device_id LIKE ?", "%"+deviceID+"%")
	}
	if status := strings.TrimSpace(ctx.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if startedAfter := strings.TrimSpace(ctx.Query("started_after")); startedAfter != "" {
		query = query.Where("started_at >= ?", startedAfter)
	}
	if startedBefore := strings.TrimSpace(ctx.Query("started_before")); startedBefore != "" {
		query = query.Where("started_at <= ?", startedBefore)
	}
	var total int64
	query.Count(&total)
	var meetings []models.Meeting
	if err := query.Order("started_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&meetings).Error; err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "查询会议失败"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": meetings, "total": total, "page": page, "page_size": pageSize})
}

func (c *MeetingController) Get(ctx *gin.Context) {
	userID, ok := ctx.Get("user_id")
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "未授权"})
		return
	}
	var meeting models.Meeting
	if err := c.DB.Where("id = ? AND user_id = ?", ctx.Param("id"), userID).First(&meeting).Error; err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "会议不存在"})
		return
	}
	var segments []models.MeetingSegment
	if err := c.DB.Where("meeting_ref_id = ?", meeting.ID).Order("start_ms ASC, id ASC").Find(&segments).Error; err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "查询会议转写失败"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"meeting": meeting, "segments": segments})
}
