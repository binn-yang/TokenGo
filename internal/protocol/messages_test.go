package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDecodeRequest(t *testing.T) {
	msg := NewRequestMessage("exit.example.com:8443", []byte("encrypted-payload"))
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Type != MessageTypeRequest {
		t.Errorf("Type = %d, want %d", decoded.Type, MessageTypeRequest)
	}
	if decoded.Target != "exit.example.com:8443" {
		t.Errorf("Target = %q, want %q", decoded.Target, "exit.example.com:8443")
	}
	if !bytes.Equal(decoded.Payload, []byte("encrypted-payload")) {
		t.Error("Payload mismatch")
	}
}

func TestEncodeDecodeStreamRequest(t *testing.T) {
	msg := NewStreamRequestMessage("exit.example.com:8443", []byte("ohttp-payload"))
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Type != MessageTypeStreamRequest {
		t.Errorf("Type = %d, want %d", decoded.Type, MessageTypeStreamRequest)
	}
	if decoded.Target != "exit.example.com:8443" {
		t.Errorf("Target = %q, want %q", decoded.Target, "exit.example.com:8443")
	}
	if !bytes.Equal(decoded.Payload, []byte("ohttp-payload")) {
		t.Error("Payload mismatch")
	}
}

func TestEncodeDecodeStreamChunk(t *testing.T) {
	payload := []byte("encrypted-sse-event-data")
	msg := NewStreamChunkMessage(payload)
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Type != MessageTypeStreamChunk {
		t.Errorf("Type = %d, want %d", decoded.Type, MessageTypeStreamChunk)
	}
	if decoded.Target != "" {
		t.Errorf("Target = %q, want empty", decoded.Target)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Error("Payload mismatch")
	}
}

func TestEncodeDecodeStreamEnd(t *testing.T) {
	msg := NewStreamEndMessage()
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Type != MessageTypeStreamEnd {
		t.Errorf("Type = %d, want %d", decoded.Type, MessageTypeStreamEnd)
	}
	if decoded.Target != "" {
		t.Errorf("Target = %q, want empty", decoded.Target)
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("Payload length = %d, want 0", len(decoded.Payload))
	}
}

func TestDecodeMultipleMessages(t *testing.T) {
	// 模拟流式场景：多个消息连续写入
	var buf bytes.Buffer

	chunk1 := NewStreamChunkMessage([]byte("chunk-1"))
	chunk2 := NewStreamChunkMessage([]byte("chunk-2"))
	end := NewStreamEndMessage()

	buf.Write(chunk1.Encode())
	buf.Write(chunk2.Encode())
	buf.Write(end.Encode())

	reader := bytes.NewReader(buf.Bytes())

	// 读取第一个 chunk
	msg1, err := Decode(reader)
	if err != nil {
		t.Fatalf("Decode chunk1 failed: %v", err)
	}
	if msg1.Type != MessageTypeStreamChunk {
		t.Errorf("msg1.Type = %d, want %d", msg1.Type, MessageTypeStreamChunk)
	}
	if !bytes.Equal(msg1.Payload, []byte("chunk-1")) {
		t.Error("msg1 payload mismatch")
	}

	// 读取第二个 chunk
	msg2, err := Decode(reader)
	if err != nil {
		t.Fatalf("Decode chunk2 failed: %v", err)
	}
	if !bytes.Equal(msg2.Payload, []byte("chunk-2")) {
		t.Error("msg2 payload mismatch")
	}

	// 读取结束标记
	msg3, err := Decode(reader)
	if err != nil {
		t.Fatalf("Decode end failed: %v", err)
	}
	if msg3.Type != MessageTypeStreamEnd {
		t.Errorf("msg3.Type = %d, want %d", msg3.Type, MessageTypeStreamEnd)
	}
}

// --- 补充消息类型测试 ---

func TestEncodeDecodeRegister(t *testing.T) {
	keyConfig := []byte("test-key-config-data")
	msg := NewRegisterMessage("abc123hash", keyConfig)
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeRegister {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeRegister)
	}
	if decoded.Target != "abc123hash" {
		t.Errorf("Target = %q, want %q", decoded.Target, "abc123hash")
	}
	if !bytes.Equal(decoded.Payload, keyConfig) {
		t.Error("Payload mismatch")
	}
}

func TestEncodeDecodeRegisterAck(t *testing.T) {
	msg := NewRegisterAckMessage([]byte("ack-payload"))
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeRegisterAck {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeRegisterAck)
	}
	if !bytes.Equal(decoded.Payload, []byte("ack-payload")) {
		t.Error("Payload mismatch")
	}
}

func TestEncodeDecodeHeartbeat(t *testing.T) {
	msg := NewHeartbeatMessage()
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeHeartbeat {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeHeartbeat)
	}
	if decoded.Target != "" {
		t.Errorf("Target = %q, want empty", decoded.Target)
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("Payload length = %d, want 0", len(decoded.Payload))
	}
}

func TestEncodeDecodeHeartbeatAck(t *testing.T) {
	msg := NewHeartbeatAckMessage()
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeHeartbeatAck {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeHeartbeatAck)
	}
}

func TestEncodeDecodeError(t *testing.T) {
	msg := NewErrorMessage("something went wrong")
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeError {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeError)
	}
	if string(decoded.Payload) != "something went wrong" {
		t.Errorf("Payload = %q, want %q", string(decoded.Payload), "something went wrong")
	}
}

func TestEncodeDecodeQueryExitKeys(t *testing.T) {
	msg := NewQueryExitKeysMessage()
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeQueryExitKeys {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeQueryExitKeys)
	}
}

func TestNewExitKeysResponseMessage(t *testing.T) {
	entries := []ExitKeyEntry{
		{PubKeyHash: "hash1", KeyConfig: []byte("kc1")},
		{PubKeyHash: "hash2", KeyConfig: []byte("kc2")},
	}

	msg, err := NewExitKeysResponseMessage(entries)
	if err != nil {
		t.Fatalf("NewExitKeysResponseMessage failed: %v", err)
	}
	if msg.Type != MessageTypeExitKeysResponse {
		t.Errorf("Type = 0x%02x, want 0x%02x", msg.Type, MessageTypeExitKeysResponse)
	}

	// 编解码完整往返
	encoded := msg.Encode()
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	// 反序列化 payload 并验证
	var got []ExitKeyEntry
	if err := json.Unmarshal(decoded.Payload, &got); err != nil {
		t.Fatalf("Unmarshal payload failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].PubKeyHash != "hash1" || got[1].PubKeyHash != "hash2" {
		t.Errorf("entry hashes mismatch: %v", got)
	}
}

func TestEncodeDecodeResponse(t *testing.T) {
	msg := NewResponseMessage([]byte("encrypted-response"))
	encoded := msg.Encode()

	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Type != MessageTypeResponse {
		t.Errorf("Type = 0x%02x, want 0x%02x", decoded.Type, MessageTypeResponse)
	}
	if decoded.Target != "" {
		t.Errorf("Target = %q, want empty", decoded.Target)
	}
	if !bytes.Equal(decoded.Payload, []byte("encrypted-response")) {
		t.Error("Payload mismatch")
	}
}

// --- 边界和错误测试 ---

func TestDecodeInvalidData_TooShort(t *testing.T) {
	// 不足 3 字节头
	_, err := Decode(bytes.NewReader([]byte{0x01}))
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestDecodeInvalidData_CorruptedPayloadLength(t *testing.T) {
	// 有效的 header (type=0x01, targetLen=0)，但 payload 长度字段只有 2 字节
	data := []byte{0x01, 0x00, 0x00, 0x00, 0x01}
	_, err := Decode(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for corrupted payload length")
	}
}

func TestDecodeMaxTargetSize(t *testing.T) {
	// Target 超过 1024 字节
	buf := make([]byte, 3)
	buf[0] = byte(MessageTypeRequest)
	binary.BigEndian.PutUint16(buf[1:3], 1025) // 超过 maxTargetSize

	_, err := Decode(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for oversized target")
	}
	if !strings.Contains(err.Error(), "过长") {
		t.Errorf("error should mention size limit, got: %v", err)
	}
}

func TestDecodeOversizedPayload(t *testing.T) {
	// Payload 声称 > 16MB
	buf := make([]byte, 7)
	buf[0] = byte(MessageTypeRequest)
	binary.BigEndian.PutUint16(buf[1:3], 0)             // target len = 0
	binary.BigEndian.PutUint32(buf[3:7], 17*1024*1024)   // > 16MB

	_, err := Decode(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if !strings.Contains(err.Error(), "过大") {
		t.Errorf("error should mention payload size, got: %v", err)
	}
}

func TestDecodeTargetTruncated(t *testing.T) {
	// 声称 target 长度 10，但只提供 3 字节
	buf := make([]byte, 6)
	buf[0] = byte(MessageTypeRequest)
	binary.BigEndian.PutUint16(buf[1:3], 10) // target len = 10
	// 只有 3 额外字节，不够

	_, err := Decode(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for truncated target")
	}
}

func TestDecodePayloadTruncated(t *testing.T) {
	// 合法的 header，payload 声称 100 字节但数据不足
	buf := make([]byte, 7)
	buf[0] = byte(MessageTypeRequest)
	binary.BigEndian.PutUint16(buf[1:3], 0)
	binary.BigEndian.PutUint32(buf[3:7], 100) // 声称 100 字节

	// 只追加 5 字节
	buf = append(buf, make([]byte, 5)...)

	_, err := Decode(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for truncated payload")
	}
}
