.PHONY: all build clean test run-client run-relay run-exit keygen help

# 版本信息
VERSION := 0.1.0
BUILD_TIME := $(shell date +%Y-%m-%d_%H:%M:%S)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# 输出目录
BUILD_DIR := build
BIN_NAME := tokengo

all: build

# 编译
build:
	@echo "编译 TokenGo..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN_NAME) ./cmd/tokengo

# 交叉编译
build-all: build-linux build-darwin build-windows

build-linux:
	@echo "编译 Linux 版本..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN_NAME)-linux-amd64 ./cmd/tokengo

build-darwin:
	@echo "编译 macOS 版本..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN_NAME)-darwin-amd64 ./cmd/tokengo
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN_NAME)-darwin-arm64 ./cmd/tokengo

build-windows:
	@echo "编译 Windows 版本..."
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN_NAME)-windows-amd64.exe ./cmd/tokengo

# 安装依赖
deps:
	go mod download
	go mod tidy

# 清理
clean:
	@echo "清理构建产物..."
	rm -rf $(BUILD_DIR)
	rm -rf keys

# 测试
test:
	go test -v ./...

# 生成测试证书 (开发用)
certs:
	@echo "生成测试证书..."
	@mkdir -p certs
	openssl req -x509 -newkey rsa:4096 -keyout certs/key.pem -out certs/cert.pem -days 365 -nodes \
		-subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
	@echo "证书已生成到 certs/ 目录"

# 生成 OHTTP 密钥
keygen: build
	@echo "生成 OHTTP 密钥..."
	./$(BUILD_DIR)/$(BIN_NAME) keygen --output ./keys

# 运行客户端
run-client: build
	./$(BUILD_DIR)/$(BIN_NAME) client --config configs/client.yaml

# 运行中继节点
run-relay: build
	./$(BUILD_DIR)/$(BIN_NAME) relay --config configs/relay-dht.yaml

# 运行出口节点
run-exit: build
	./$(BUILD_DIR)/$(BIN_NAME) exit --config configs/exit-dht.yaml

# 本地测试 (启动所有组件)
# 需要分别在不同终端运行
local-test: build certs keygen
	@echo "本地测试环境准备完成"
	@echo ""
	@echo "请在不同终端分别运行:"
	@echo "  1. make run-exit   (出口节点)"
	@echo "  2. make run-relay  (中继节点)"
	@echo "  3. make run-client (客户端)"
	@echo ""
	@echo "然后测试:"
	@echo '  curl http://127.0.0.1:8080/v1/chat/completions \'
	@echo '    -H "Content-Type: application/json" \'
	@echo '    -d '\''{"model":"llama2","messages":[{"role":"user","content":"Hello"}]}'\'''
# ==================== 帮助 ====================

# 帮助
help:
	@echo "TokenGo - 去中心化 AI API 网关"
	@echo ""
	@echo "本地开发命令:"
	@echo "  make build       - 编译项目"
	@echo "  make build-all   - 交叉编译所有平台"
	@echo "  make deps        - 安装依赖"
	@echo "  make clean       - 清理构建产物"
	@echo "  make test        - 运行测试"
	@echo "  make certs       - 生成测试证书"
	@echo "  make keygen      - 生成 OHTTP 密钥"
	@echo "  make run-client  - 运行客户端"
	@echo "  make run-relay   - 运行中继节点"
	@echo "  make run-exit    - 运行出口节点"
	@echo "  make local-test  - 准备本地测试环境"
	@echo ""
	@echo "  make help        - 显示帮助"
