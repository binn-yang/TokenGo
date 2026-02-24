package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// MessageType 消息类型
type MessageType uint8

const (
	// MessageTypeRequest OHTTP 请求
	MessageTypeRequest MessageType = 0x01
	// MessageTypeResponse OHTTP 响应
	MessageTypeResponse MessageType = 0x02
	// MessageTypeStreamRequest 流式请求 (格式同 Request，仅类型不同)
	MessageTypeStreamRequest MessageType = 0x03
	// MessageTypeStreamChunk 流式响应块 (加密后的 SSE 事件)
	MessageTypeStreamChunk MessageType = 0x04
	// MessageTypeStreamEnd 流式结束标记
	MessageTypeStreamEnd MessageType = 0x05

	// MessageTypeRegister Exit→Relay 注册 (Target=pubKeyHash)
	MessageTypeRegister MessageType = 0x10
	// MessageTypeRegisterAck Relay→Exit 注册确认
	MessageTypeRegisterAck MessageType = 0x11

	// MessageTypeQueryExitKeys Client→Relay: 查询 Exit 公钥列表
	MessageTypeQueryExitKeys MessageType = 0x12
	// MessageTypeExitKeysResponse Relay→Client: 返回 Exit 公钥列表
	MessageTypeExitKeysResponse MessageType = 0x13

	// MessageTypeHeartbeat Exit→Relay 心跳
	MessageTypeHeartbeat MessageType = 0x20
	// MessageTypeHeartbeatAck Relay→Exit 心跳确认
	MessageTypeHeartbeatAck MessageType = 0x21

	// MessageTypeError 错误消息
	MessageTypeError MessageType = 0xFF
)

// Message 通用消息结构
type Message struct {
	Type    MessageType
	Target  string // 目标标识 (请求消息中为 Exit pubKeyHash，注册消息中为 pubKeyHash)
	Payload []byte
}

// Encode 编码消息为字节流
// 格式: [Type(1)] [TargetLen(2)] [Target(N)] [PayloadLen(4)] [Payload(N)]
func (m *Message) Encode() []byte {
	targetBytes := []byte(m.Target)
	buf := make([]byte, 1+2+len(targetBytes)+4+len(m.Payload))
	buf[0] = byte(m.Type)
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(targetBytes)))
	copy(buf[3:3+len(targetBytes)], targetBytes)
	binary.BigEndian.PutUint32(buf[3+len(targetBytes):7+len(targetBytes)], uint32(len(m.Payload)))
	copy(buf[7+len(targetBytes):], m.Payload)
	return buf
}

// Decode 从字节流解码消息
// 格式: [Type(1)] [TargetLen(2)] [Target(N)] [PayloadLen(4)] [Payload(N)]
func Decode(r io.Reader) (*Message, error) {
	// 读取类型和目标长度
	header := make([]byte, 3)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("读取消息头失败: %w", err)
	}

	msgType := MessageType(header[0])
	targetLen := binary.BigEndian.Uint16(header[1:3])

	// 限制目标地址最大长度 (1KB)
	const maxTargetSize = 1024
	if targetLen > maxTargetSize {
		return nil, fmt.Errorf("目标地址过长: %d > %d", targetLen, maxTargetSize)
	}

	// 读取目标地址
	target := make([]byte, targetLen)
	if targetLen > 0 {
		if _, err := io.ReadFull(r, target); err != nil {
			return nil, fmt.Errorf("读取目标地址失败: %w", err)
		}
	}

	// 读取负载长度
	payloadLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, payloadLenBuf); err != nil {
		return nil, fmt.Errorf("读取负载长度失败: %w", err)
	}
	payloadLen := binary.BigEndian.Uint32(payloadLenBuf)

	// 限制最大负载大小 (16MB)
	const maxPayloadSize = 16 * 1024 * 1024
	if payloadLen > maxPayloadSize {
		return nil, fmt.Errorf("负载过大: %d > %d", payloadLen, maxPayloadSize)
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("读取负载失败: %w", err)
	}

	return &Message{
		Type:    msgType,
		Target:  string(target),
		Payload: payload,
	}, nil
}

// NewRequestMessage 创建请求消息
func NewRequestMessage(target string, ohttpPayload []byte) *Message {
	return &Message{
		Type:    MessageTypeRequest,
		Target:  target,
		Payload: ohttpPayload,
	}
}

// NewResponseMessage 创建响应消息
func NewResponseMessage(ohttpPayload []byte) *Message {
	return &Message{
		Type:    MessageTypeResponse,
		Payload: ohttpPayload,
	}
}

// NewStreamRequestMessage 创建流式请求消息
func NewStreamRequestMessage(target string, ohttpPayload []byte) *Message {
	return &Message{
		Type:    MessageTypeStreamRequest,
		Target:  target,
		Payload: ohttpPayload,
	}
}

// NewStreamChunkMessage 创建流式响应块消息
func NewStreamChunkMessage(encryptedChunk []byte) *Message {
	return &Message{
		Type:    MessageTypeStreamChunk,
		Payload: encryptedChunk,
	}
}

// NewStreamEndMessage 创建流式结束标记消息
func NewStreamEndMessage() *Message {
	return &Message{
		Type: MessageTypeStreamEnd,
	}
}

// NewErrorMessage 创建错误消息
func NewErrorMessage(errMsg string) *Message {
	return &Message{
		Type:    MessageTypeError,
		Payload: []byte(errMsg),
	}
}

// NewRegisterMessage 创建 Exit 注册消息
func NewRegisterMessage(pubKeyHash string, payload []byte) *Message {
	return &Message{
		Type:    MessageTypeRegister,
		Target:  pubKeyHash,
		Payload: payload,
	}
}

// NewRegisterAckMessage 创建注册确认消息
func NewRegisterAckMessage(payload []byte) *Message {
	return &Message{
		Type:    MessageTypeRegisterAck,
		Payload: payload,
	}
}

// NewHeartbeatMessage 创建心跳消息
func NewHeartbeatMessage() *Message {
	return &Message{
		Type: MessageTypeHeartbeat,
	}
}

// NewHeartbeatAckMessage 创建心跳确认消息
func NewHeartbeatAckMessage() *Message {
	return &Message{
		Type: MessageTypeHeartbeatAck,
	}
}

// ExitKeyEntry Exit 公钥条目 (用于 Relay 返回给 Client)
type ExitKeyEntry struct {
	PubKeyHash string `json:"pub_key_hash"`
	KeyConfig  []byte `json:"key_config"` // OHTTP KeyConfig 编码 (RFC 9458)
}

// NewQueryExitKeysMessage 创建查询 Exit 公钥列表消息 (Client → Relay)
func NewQueryExitKeysMessage() *Message {
	return &Message{
		Type: MessageTypeQueryExitKeys,
	}
}

// NewExitKeysResponseMessage 创建 Exit 公钥列表响应消息 (Relay → Client)
func NewExitKeysResponseMessage(entries []ExitKeyEntry) *Message {
	data, _ := json.Marshal(entries)
	return &Message{
		Type:    MessageTypeExitKeysResponse,
		Payload: data,
	}
}
