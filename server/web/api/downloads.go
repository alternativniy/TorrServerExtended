package api

import (
	"net/http"
	"strconv"

	"server/torr"
	"server/web/api/utils"

	"github.com/gin-gonic/gin"
)

type downloadCreateRequest struct {
	Link       string `json:"link"`
	Files      []int  `json:"files"`
	TargetPath string `json:"target_path"`
	Title      string `json:"title"`
	Poster     string `json:"poster"`
	Category   string `json:"category"`
	Data       string `json:"data"`
}

func downloadCreate(c *gin.Context) {
	var req downloadCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Link == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "link is required"})
		return
	}

	spec, err := utils.ParseLink(req.Link)
	if err != nil {
		c.AbortWithError(http.StatusBadRequest, err)
		return
	}

	hash := spec.InfoHash.HexString()
	tor := torr.GetTorrent(hash)
	if tor == nil {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "torrent not found", "hash": hash})
		return
	}

	job, err := torr.CreateDownloadJobForTorrent(tor, req.Title, req.Files)
	if err != nil {
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, job)
}

func downloadList(c *gin.Context) {
	c.JSON(http.StatusOK, torr.ListDownloadJobs())
}

func downloadGet(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	job := torr.GetDownloadJob(id)
	if job == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, job)
}

func downloadDelete(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	deleteFiles, _ := strconv.ParseBool(c.DefaultQuery("remove_files", "false"))

	canceled := torr.CancelDownloadJob(id)
	removed := torr.RemoveDownloadJob(id, deleteFiles, false)
	if !removed && !canceled {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Status(http.StatusNoContent)
}

func downloadPause(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	if !torr.PauseDownloadJob(id) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}

func downloadResume(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	if err := torr.ResumeDownloadJob(id); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusAccepted)
}

func downloadStop(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	if !torr.CancelDownloadJob(id) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Status(http.StatusNoContent)
}
