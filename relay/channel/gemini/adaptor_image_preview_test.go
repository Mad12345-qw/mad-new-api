package gemini

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertGeminiImagePreviewRequest(t *testing.T) {
	request, err := convertGeminiImagePreviewRequest(nil, &relaycommon.RelayInfo{OriginModelName: "gemini-3.1-flash-image-preview"}, dto.ImageRequest{
		Prompt:  "draw a test image",
		Size:    "16:9",
		Quality: "2K",
	})
	require.NoError(t, err)
	require.Len(t, request.Contents, 1)
	require.Equal(t, "user", request.Contents[0].Role)
	require.Equal(t, "draw a test image", request.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, request.GenerationConfig.ResponseModalities)
	require.JSONEq(t, `{"aspectRatio":"16:9","imageSize":"2K"}`, string(request.GenerationConfig.ImageConfig))
}

func TestConvertGeminiImagePreviewRequestPreservesReferenceOrderAndFourKSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, raw := range [][]byte{[]byte("first-image"), []byte("second-image")} {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="reference-%d.png"`, index+1))
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(raw)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	httpRequest := httptest.NewRequest("POST", "/v1/images/edits", &body)
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, httpRequest.ParseMultipartForm(32<<20))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httpRequest

	request, err := convertGeminiImagePreviewRequest(c, &relaycommon.RelayInfo{
		OriginModelName: "gemini-3.1-flash-image-preview-4K",
	}, dto.ImageRequest{
		Prompt: "use image 1 as the person and image 2 as the clothes",
		Size:   "2448x3264",
	})
	require.NoError(t, err)
	require.Len(t, request.Contents, 1)
	require.Len(t, request.Contents[0].Parts, 6)
	require.Contains(t, request.Contents[0].Parts[0].Text, "不得根据图片内容重新排序")
	require.Equal(t, "图1（第1个上传的参考图；紧随其后的图片数据就是图1）", request.Contents[0].Parts[1].Text)
	require.Equal(t, "Zmlyc3QtaW1hZ2U=", request.Contents[0].Parts[2].InlineData.Data)
	require.Equal(t, "图2（第2个上传的参考图；紧随其后的图片数据就是图2）", request.Contents[0].Parts[3].Text)
	require.Equal(t, "c2Vjb25kLWltYWdl", request.Contents[0].Parts[4].InlineData.Data)
	require.Equal(t, "用户原始提示词（其中“图N”严格对应以上相同编号）：\nuse image 1 as the person and image 2 as the clothes", request.Contents[0].Parts[5].Text)
	require.JSONEq(t, `{"aspectRatio":"3:4","imageSize":"4K"}`, string(request.GenerationConfig.ImageConfig))
}

func TestConvertGeminiImagePreviewJSONReferencesPreserveArrayOrder(t *testing.T) {
	request, err := convertGeminiImagePreviewRequest(nil, &relaycommon.RelayInfo{
		OriginModelName: "gemini-3.1-flash-image-preview",
	}, dto.ImageRequest{
		Prompt:  "以图1为人物基础，以图2为服装参考",
		Images:  []byte(`["data:image/png;base64,Zmlyc3QtaW1hZ2U=","data:image/jpeg;base64,c2Vjb25kLWltYWdl"]`),
		Size:    "3:4",
		Quality: "2K",
	})
	require.NoError(t, err)
	require.Len(t, request.Contents[0].Parts, 6)
	require.Equal(t, "图1（第1个上传的参考图；紧随其后的图片数据就是图1）", request.Contents[0].Parts[1].Text)
	require.Equal(t, "Zmlyc3QtaW1hZ2U=", request.Contents[0].Parts[2].InlineData.Data)
	require.Equal(t, "image/png", request.Contents[0].Parts[2].InlineData.MimeType)
	require.Equal(t, "图2（第2个上传的参考图；紧随其后的图片数据就是图2）", request.Contents[0].Parts[3].Text)
	require.Equal(t, "c2Vjb25kLWltYWdl", request.Contents[0].Parts[4].InlineData.Data)
	require.Equal(t, "image/jpeg", request.Contents[0].Parts[4].InlineData.MimeType)
	require.Contains(t, request.Contents[0].Parts[5].Text, "以图1为人物基础，以图2为服装参考")
	require.JSONEq(t, `{"aspectRatio":"3:4","imageSize":"2K"}`, string(request.GenerationConfig.ImageConfig))
}

func TestGeminiImagePreviewModelMatrix(t *testing.T) {
	require.True(t, isGeminiImagePreviewModel("gemini-3.1-flash-image-preview"))
	require.True(t, isGeminiImagePreviewModel("gemini-3-pro-image-preview"))
	require.False(t, isGeminiImagePreviewModel("imagen-4"))
}
