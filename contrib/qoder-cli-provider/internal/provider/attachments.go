package provider

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	maxAttachmentCount = 10
	maxAttachmentBytes = 20 * 1024 * 1024
)

type Attachment struct {
	FileName  string
	MediaType string
	Data      []byte
}

func appendBase64Image(attachments *[]Attachment, mediaType, encoded string) (string, error) {
	if attachments == nil {
		return "", fmt.Errorf("image attachment collector is unavailable")
	}
	if len(*attachments) >= maxAttachmentCount {
		return "", fmt.Errorf("image attachment count exceeds %d", maxAttachmentCount)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	extension, ok := imageExtension(mediaType)
	if !ok {
		return "", fmt.Errorf("unsupported image media type %q", mediaType)
	}
	encoded = strings.TrimSpace(encoded)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return "", fmt.Errorf("decode %s image attachment: %w", mediaType, err)
	}
	total := len(data)
	for _, attachment := range *attachments {
		total += len(attachment.Data)
	}
	if total > maxAttachmentBytes {
		return "", fmt.Errorf("image attachments exceed %d bytes", maxAttachmentBytes)
	}
	name := fmt.Sprintf("image-%03d%s", len(*attachments)+1, extension)
	*attachments = append(*attachments, Attachment{FileName: name, MediaType: mediaType, Data: data})
	return "[Attached image: " + name + "]", nil
}

func imageExtension(mediaType string) (string, bool) {
	switch mediaType {
	case "image/png":
		return ".png", true
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	default:
		return "", false
	}
}

func parseDataImageURL(value string, attachments *[]Attachment) (string, bool, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "data:image/") {
		return "", false, nil
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", true, fmt.Errorf("image data URL must use base64 encoding")
	}
	mediaType := strings.TrimSpace(strings.TrimSuffix(header[len("data:"):], ";base64"))
	marker, err := appendBase64Image(attachments, mediaType, encoded)
	return marker, true, err
}
