package llm

// collectImageInputParts 收集消息中可发送给图片编辑类协议的原始图片输入。
func collectImageInputParts(messages []Message) []ContentPart {
	images := make([]ContentPart, 0)
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Kind != ContentPartImage || len(part.Data) == 0 {
				continue
			}
			images = append(images, part)
		}
	}
	return images
}
