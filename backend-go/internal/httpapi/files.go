package httpapi

import (
	"net/http"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"
	"github.com/gin-gonic/gin"
)

func registerFileAPI(api *gin.RouterGroup, cfg config.Config, store *service.Store) {
	api.GET("/files", func(c *gin.Context) {
		clusterID, ok := queryIntOpt(c, "cluster_id")
		if !ok {
			return
		}
		files, err := service.ListTransferFiles(cfg.TransferDir, store.Apps(clusterID))
		if err != nil {
			fail(c, http.StatusInternalServerError, "读取文件列表失败: "+err.Error())
			return
		}
		c.JSON(http.StatusOK, files)
	})

	api.GET("/files/parse-version", func(c *gin.Context) {
		fileName := c.Query("file_name")
		if fileName == "" {
			fail(c, http.StatusBadRequest, "file_name 为必填参数")
			return
		}
		c.JSON(http.StatusOK, gin.H{"version": service.ParseVersionFromFilename(fileName)})
	})
}
