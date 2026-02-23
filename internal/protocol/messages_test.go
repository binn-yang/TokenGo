package protocol

import (
	"bytes"
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
