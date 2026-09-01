package moirai

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	agUserInput       = 14
	agPlannerResponse = 15
	agGenericTool     = 132
)

type AntigravityCodec struct{}

func (AntigravityCodec) Format() Format { return FormatAntigravity }
func (AntigravityCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatAntigravity, DisplayName: "Antigravity CLI", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

type protoField struct {
	number uint32
	value  uint64
	bytes  []byte
	wire   uint8
}

func protoFields(data []byte) []protoField {
	var result []protoField
	for offset := 0; offset < len(data); {
		tag, next, ok := protoReadVarint(data, offset)
		if !ok || tag>>3 == 0 {
			break
		}
		field := protoField{number: uint32(tag >> 3), wire: uint8(tag & 7)}
		offset = next
		switch field.wire {
		case 0:
			value, next, ok := protoReadVarint(data, offset)
			if !ok {
				return result
			}
			field.value, offset = value, next
		case 2:
			length, next, ok := protoReadVarint(data, offset)
			if !ok || length > uint64(len(data)-next) {
				return result
			}
			end := next + int(length)
			field.bytes, offset = data[next:end], end
		case 1:
			if offset+8 > len(data) {
				return result
			}
			field.bytes, offset = data[offset:offset+8], offset+8
		case 5:
			if offset+4 > len(data) {
				return result
			}
			field.bytes, offset = data[offset:offset+4], offset+4
		default:
			return result
		}
		result = append(result, field)
	}
	return result
}

func protoReadVarint(data []byte, offset int) (uint64, int, bool) {
	var value uint64
	for shift := uint(0); shift < 64 && offset < len(data); shift += 7 {
		b := data[offset]
		offset++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, offset, true
		}
	}
	return 0, offset, false
}

func protoBytes(fields []protoField, number uint32) []byte {
	for _, field := range fields {
		if field.number == number && field.wire == 2 {
			return field.bytes
		}
	}
	return nil
}

func protoAllBytes(fields []protoField, number uint32) [][]byte {
	var values [][]byte
	for _, field := range fields {
		if field.number == number && field.wire == 2 {
			values = append(values, field.bytes)
		}
	}
	return values
}

func protoString(fields []protoField, number uint32) string {
	return string(protoBytes(fields, number))
}

func protoUint(fields []protoField, number uint32) uint64 {
	for _, field := range fields {
		if field.number == number && field.wire == 0 {
			return field.value
		}
	}
	return 0
}

func protoTimestamp(data []byte) string {
	fields := protoFields(data)
	seconds := int64(protoUint(fields, 1))
	nanos := int64(protoUint(fields, 2))
	if seconds == 0 && nanos == 0 {
		return ""
	}
	return time.Unix(seconds, nanos).UTC().Format(time.RFC3339Nano)
}

func (AntigravityCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	metaRows := array(body["trajectory_meta"])
	id := ""
	if len(metaRows) > 0 {
		id = stringValue(object(metaRows[0])["cascade_id"])
	}
	var main []byte
	for _, raw := range array(body["trajectory_metadata_blob"]) {
		row := object(raw)
		if stringValue(row["id"]) == "main" {
			main, _ = hex.DecodeString(stringValue(row["data"]))
		}
	}
	mainFields := protoFields(main)
	workspace := protoFields(protoBytes(mainFields, 1))
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: firstNonEmpty(id, protoString(mainFields, 6)), Timestamp: protoTimestamp(protoBytes(mainFields, 2)), CWD: strings.TrimPrefix(firstNonEmpty(protoString(workspace, 1), protoString(mainFields, 7)), "file://"), GitBranch: protoString(workspace, 4)}}
	steps := array(body["steps"])
	sort.SliceStable(steps, func(i, j int) bool {
		return integerValue(object(steps[i])["idx"]) < integerValue(object(steps[j])["idx"])
	})
	known := map[string]bool{}
	var warnings []Warning
	for si, rawStep := range steps {
		step := object(rawStep)
		payloadBytes, decodeErr := hex.DecodeString(stringValue(step["step_payload"]))
		if decodeErr != nil {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("steps[%d]", si), Code: "invalid_payload", Message: "step omitted"})
			continue
		}
		payload := protoFields(payloadBytes)
		metadata := protoFields(protoBytes(payload, 5))
		stamp := protoTimestamp(protoBytes(metadata, 1))
		stepType := integerValue(step["step_type"])
		switch stepType {
		case agUserInput:
			input := protoFields(protoBytes(payload, 19))
			text := firstNonEmpty(protoString(input, 2), protoString(input, 1))
			var blocks []Block
			if text != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
			for _, rawImage := range protoAllBytes(input, 5) {
				image := protoFields(rawImage)
				blocks = append(blocks, Block{Type: BlockImage, Source: &MediaSource{Type: "base64", MediaType: firstNonEmpty(protoString(image, 2), "image/png"), Data: protoString(image, 1)}})
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleUser, Content: blocks, Timestamp: stamp})
			}
		case agPlannerResponse:
			planner := protoFields(protoBytes(payload, 20))
			var blocks []Block
			if thinking := protoString(planner, 3); thinking != "" {
				blocks = append(blocks, Block{Type: BlockThinking, Text: thinking, Signature: protoString(planner, 4)})
			}
			if text := protoString(planner, 1); text != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
			for ci, rawCall := range protoAllBytes(planner, 7) {
				call := protoFields(rawCall)
				id := firstNonEmpty(protoString(call, 1), syntheticToolID(si, ci))
				name := protoString(call, 2)
				if name == "" {
					continue
				}
				var input any = map[string]any{}
				if encoded := protoString(call, 3); json.Valid([]byte(encoded)) {
					_ = json.Unmarshal([]byte(encoded), &input)
				}
				blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: name, Input: rawJSON(input)})
				known[id] = true
			}
			if len(blocks) > 0 {
				usageFields := protoFields(protoBytes(metadata, 9))
				usage := &Usage{InputTokens: int64(protoUint(usageFields, 2)), OutputTokens: int64(protoUint(usageFields, 3)), CacheCreationInputTokens: int64(protoUint(usageFields, 4)), CacheReadInputTokens: int64(protoUint(usageFields, 5))}
				if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
					usage = nil
				}
				model := ""
				if modelID := protoUint(metadata, 11); modelID > 0 {
					model = fmt.Sprintf("model-%d", modelID)
				}
				message := Message{Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: model, StopReason: agStopReason(protoUint(planner, 12)), Usage: usage}
				t.Messages = append(t.Messages, message)
				t.Meta.Model = firstNonEmpty(t.Meta.Model, model)
			}
		default:
			call := protoFields(protoBytes(metadata, 4))
			callID := protoString(call, 1)
			status := integerValue(step["status"])
			if callID == "" || status == 1 {
				continue
			}
			if !known[callID] {
				warnings = append(warnings, Warning{Path: fmt.Sprintf("steps[%d]", si), Code: "unpaired_tool_result", Message: "result omitted"})
				continue
			}
			result := protoFields(protoBytes(payload, 140))
			resultEnvelope := protoFields(protoBytes(result, 2))
			output := protoString(resultEnvelope, 1)
			var content any = output
			if json.Valid([]byte(output)) {
				_ = json.Unmarshal([]byte(output), &content)
			}
			t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: callID, Content: rawJSON(content), IsError: status == 6 || status == 7}}, Timestamp: stamp})
		}
	}
	if len(t.Messages) == 0 {
		return nil, fmt.Errorf("%w: no messages", ErrInvalidTranscript)
	}
	if err := normalizeTranscript(t, opts.SourceID, ""); err != nil {
		return nil, err
	}
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t, Warnings: warnings}, nil
}

func agStopReason(value uint64) string {
	switch value {
	case 2:
		return "end_turn"
	case 3:
		return "max_tokens"
	case 10:
		return "tool_use"
	case 13:
		return "error"
	case 16:
		return "aborted"
	case 0:
		return ""
	default:
		return fmt.Sprintf("stop-%d", value)
	}
}

func agStopValue(reason string) uint64 {
	switch reason {
	case "end_turn", "stop":
		return 2
	case "max_tokens", "length":
		return 3
	case "tool_use":
		return 10
	case "error":
		return 13
	case "aborted", "cancelled":
		return 16
	default:
		return 0
	}
}

func protoWriteVarint(output *[]byte, value uint64) {
	for value >= 0x80 {
		*output = append(*output, byte(value)|0x80)
		value >>= 7
	}
	*output = append(*output, byte(value))
}

func protoWriteUint(output *[]byte, field uint32, value uint64) {
	protoWriteVarint(output, uint64(field)<<3)
	protoWriteVarint(output, value)
}

func protoWriteBytes(output *[]byte, field uint32, value []byte) {
	protoWriteVarint(output, uint64(field)<<3|2)
	protoWriteVarint(output, uint64(len(value)))
	*output = append(*output, value...)
}

func protoWriteString(output *[]byte, field uint32, value string) {
	if value != "" {
		protoWriteBytes(output, field, []byte(value))
	}
}

func protoWriteTimestamp(output *[]byte, field uint32, stamp string) {
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return
	}
	var value []byte
	protoWriteUint(&value, 1, uint64(parsed.Unix()))
	if parsed.Nanosecond() > 0 {
		protoWriteUint(&value, 2, uint64(parsed.Nanosecond()))
	}
	protoWriteBytes(output, field, value)
}

func agMetadata(sessionID, trajectoryID string, index, turn int, stamp string, call []byte, message *Message) []byte {
	var output []byte
	protoWriteTimestamp(&output, 1, stamp)
	protoWriteUint(&output, 3, 2)
	if len(call) > 0 {
		protoWriteBytes(&output, 4, call)
	}
	if message != nil && message.Usage != nil {
		var usage []byte
		protoWriteUint(&usage, 2, uint64(message.Usage.InputTokens))
		protoWriteUint(&usage, 3, uint64(message.Usage.OutputTokens))
		protoWriteUint(&usage, 4, uint64(message.Usage.CacheCreationInputTokens))
		protoWriteUint(&usage, 5, uint64(message.Usage.CacheReadInputTokens))
		protoWriteBytes(&output, 9, usage)
	}
	if message != nil && strings.HasPrefix(message.Model, "model-") {
		if id, err := strconv.ParseUint(strings.TrimPrefix(message.Model, "model-"), 10, 64); err == nil {
			protoWriteUint(&output, 11, id)
		}
	}
	protoWriteString(&output, 12, uuidFromSeed(sessionID, fmt.Sprint(turn), "exec"))
	var info []byte
	protoWriteString(&info, 1, trajectoryID)
	if index > 0 {
		protoWriteUint(&info, 2, uint64(index))
	}
	if turn > 0 {
		protoWriteUint(&info, 3, uint64(turn-1))
	}
	protoWriteString(&info, 4, sessionID)
	protoWriteBytes(&output, 20, info)
	protoWriteUint(&output, 21, 1)
	return output
}

func agCall(block Block) []byte {
	var call []byte
	protoWriteString(&call, 1, block.ID)
	protoWriteString(&call, 2, block.Name)
	protoWriteString(&call, 3, string(block.Input))
	protoWriteString(&call, 9, block.Name)
	return call
}

func agStep(index, stepType, status int, metadata, body []byte, bodyField uint32) map[string]any {
	var payload []byte
	protoWriteUint(&payload, 1, uint64(stepType))
	protoWriteUint(&payload, 4, uint64(status))
	protoWriteBytes(&payload, 5, metadata)
	protoWriteBytes(&payload, bodyField, body)
	return map[string]any{"idx": index, "step_type": stepType, "status": status, "has_subtrajectory": false, "metadata": hex.EncodeToString(metadata), "error_details": "", "permissions": "", "task_details": "", "render_info": "", "step_payload": hex.EncodeToString(payload), "step_format": 0}
}

func (AntigravityCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	sessionID := firstNonEmpty(opts.ID, t.Meta.ID)
	trajectoryID := uuidFromSeed(sessionID, "trajectory")
	results := map[string]Block{}
	resultStamps := map[string]string{}
	for _, message := range t.Messages {
		for _, block := range message.Content {
			if block.Type == BlockToolResult {
				results[block.ToolUseID] = block
				resultStamps[block.ToolUseID] = message.Timestamp
			}
		}
	}
	var steps []any
	turn := 0
	for _, message := range t.Messages {
		stamp := firstNonEmpty(message.Timestamp, t.Meta.Timestamp)
		if message.Role == RoleUser {
			text := joinedBlocks(message.Content, BlockText)
			var images []*MediaSource
			for _, block := range message.Content {
				if block.Type == BlockImage && block.Source != nil {
					images = append(images, block.Source)
				}
			}
			if text == "" && len(images) == 0 {
				continue
			}
			turn++
			var body []byte
			protoWriteString(&body, 2, text)
			if text != "" {
				var item []byte
				protoWriteString(&item, 1, text)
				protoWriteBytes(&body, 3, item)
			}
			for _, image := range images {
				var encoded []byte
				protoWriteString(&encoded, 1, image.Data)
				protoWriteString(&encoded, 2, image.MediaType)
				protoWriteBytes(&body, 5, encoded)
			}
			metadata := agMetadata(sessionID, trajectoryID, len(steps), turn, stamp, nil, nil)
			steps = append(steps, agStep(len(steps), agUserInput, 3, metadata, body, 19))
			continue
		}
		var planner []byte
		protoWriteString(&planner, 1, joinedBlocks(message.Content, BlockText))
		for _, block := range message.Content {
			if block.Type == BlockThinking {
				protoWriteString(&planner, 3, block.Text)
				protoWriteString(&planner, 4, block.Signature)
			}
			if block.Type == BlockToolUse {
				protoWriteBytes(&planner, 7, agCall(block))
			}
		}
		protoWriteUint(&planner, 12, agStopValue(message.StopReason))
		metadata := agMetadata(sessionID, trajectoryID, len(steps), turn, stamp, nil, &message)
		steps = append(steps, agStep(len(steps), agPlannerResponse, 3, metadata, planner, 20))
		for _, call := range message.Content {
			if call.Type != BlockToolUse {
				continue
			}
			result, exists := results[call.ID]
			status := 1
			var generic []byte
			if exists {
				status = 3
				if result.IsError {
					status = 7
				}
				var output any
				_ = json.Unmarshal(result.Content, &output)
				var envelope []byte
				protoWriteString(&envelope, 1, fmt.Sprint(output))
				protoWriteBytes(&generic, 2, envelope)
			}
			resultStamp := firstNonEmpty(resultStamps[call.ID], stamp)
			metadata := agMetadata(sessionID, trajectoryID, len(steps), turn, resultStamp, agCall(call), nil)
			steps = append(steps, agStep(len(steps), agGenericTool, status, metadata, generic, 140))
		}
	}
	var workspace []byte
	if t.Meta.CWD != "" {
		protoWriteString(&workspace, 1, "file://"+t.Meta.CWD)
		protoWriteString(&workspace, 2, "file://"+t.Meta.CWD)
		protoWriteString(&workspace, 4, t.Meta.GitBranch)
	}
	var trajectoryMetadata []byte
	if len(workspace) > 0 {
		protoWriteBytes(&trajectoryMetadata, 1, workspace)
		protoWriteString(&trajectoryMetadata, 7, "file://"+t.Meta.CWD)
	}
	protoWriteTimestamp(&trajectoryMetadata, 2, t.Meta.Timestamp)
	protoWriteString(&trajectoryMetadata, 6, sessionID)
	protoWriteString(&trajectoryMetadata, 18, "default-cli-project")
	body := map[string]any{"trajectory_meta": []any{map[string]any{"trajectory_id": trajectoryID, "cascade_id": sessionID, "trajectory_type": 4, "source": 17}}, "steps": steps, "trajectory_metadata_blob": []any{map[string]any{"id": "main", "data": hex.EncodeToString(trajectoryMetadata)}}, "gen_metadata": []any{}, "executor_metadata": []any{}, "parent_references": []any{}, "battle_mode_infos": []any{}, "transcript": agDisplayLog(steps), "transcript_full": agDisplayLog(steps)}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatAntigravity, result, err)
}

func agDisplayLog(steps []any) string {
	var output strings.Builder
	for _, raw := range steps {
		step := object(raw)
		line := map[string]any{"step_index": step["idx"], "status": map[bool]string{true: "ERROR", false: "DONE"}[integerValue(step["status"]) == 7]}
		switch integerValue(step["step_type"]) {
		case agUserInput:
			line["source"], line["type"] = "USER_EXPLICIT", "USER_INPUT"
		case agPlannerResponse:
			line["source"], line["type"] = "MODEL", "PLANNER_RESPONSE"
		default:
			line["source"], line["type"] = "MODEL", "GENERIC"
		}
		encoded, _ := json.Marshal(line)
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.String()
}

func init() { _ = Register(AntigravityCodec{}) }
