package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// BodyStorageCleanup 请求体存储清理中间件
// 在请求处理完成后自动清理磁盘/内存缓存
func BodyStorageCleanup() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			// 请求结束后清理存储
			common.CleanupBodyStorage(c)

			// 清理文件缓存（URL 下载的文件等）
			service.CleanupFileSources(c)

			// 清理解析过的 multipart 表单：文件字段超过 maxMemory 的部分落在
			// os.TempDir() 的 multipart-* 临时文件里。net/http 只会删除它自己持有的
			// 那个 *http.Request 上的表单，而 RequestId 中间件已经用 WithContext 换成了
			// 副本，所以这里不删的话，上游失败、重试耗尽或提前返回的请求会把临时文件
			// 永久留在磁盘上（newapi-custom 13 天积了 30 万个、871 GB）。
			if form := c.Request.MultipartForm; form != nil {
				_ = form.RemoveAll()
				c.Request.MultipartForm = nil
			}
		}()
		// 处理请求
		c.Next()
	}
}
