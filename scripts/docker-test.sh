#!/bin/bash
# TokenGo Docker Integration Test Script

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

echo "========================================"
echo "  TokenGo Docker Integration Test"
echo "========================================"
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print status
print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if Docker is available
if ! command -v docker &> /dev/null; then
    print_error "Docker is not installed or not in PATH"
    exit 1
fi

if ! docker info &> /dev/null; then
    print_error "Docker daemon is not running"
    exit 1
fi

# Update client config with current public key
print_status "Syncing OHTTP public key to client config..."
if [ -f "keys/ohttp_private.key.pub" ]; then
    PUBLIC_KEY=$(cat keys/ohttp_private.key.pub)
    # Update docker client config
    if [ -f "configs/docker/client.yaml" ]; then
        sed -i.bak "s|exit_public_key:.*|exit_public_key: \"${PUBLIC_KEY}\"|" configs/docker/client.yaml
        rm -f configs/docker/client.yaml.bak
        print_status "Updated configs/docker/client.yaml with public key"
    fi
fi

# Build and start services
print_status "Building Docker images..."
docker compose build

print_status "Starting services..."
docker compose up -d

# Wait for services to be ready
print_status "Waiting for services to start (this may take a while for Ollama)..."
sleep 5

# Check if all services are running
print_status "Checking service status..."
docker compose ps

# Wait for Ollama to be healthy
print_status "Waiting for Ollama to be ready..."
MAX_RETRIES=30
RETRY_COUNT=0
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    # Check from host since Ollama container doesn't have curl
    if curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; then
        print_status "Ollama is ready!"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT + 1))
    echo -n "."
    sleep 2
done
echo ""

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    print_warning "Ollama may not be fully ready, but continuing with test..."
fi

# Check and pull model if needed
MODEL="llama3.2:1b"
print_status "Checking if model ${MODEL} is available..."
if ! docker exec tokengo-ollama ollama list 2>/dev/null | grep -q "llama3.2:1b"; then
    print_status "Pulling model ${MODEL} (this may take a while)..."
    docker exec tokengo-ollama ollama pull ${MODEL}
fi

# Additional wait for other services
print_status "Waiting for TokenGo services to initialize..."
sleep 5

# Run API test
print_status "Running API test..."
echo ""

RESULT=$(curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{"model":"llama3.2:1b","messages":[{"role":"user","content":"say hello"}]}' \
    --max-time 120 2>&1) || true

echo "Response:"
echo "$RESULT" | head -c 500
echo ""
echo ""

# Check result
if echo "$RESULT" | grep -q "choices"; then
    echo "========================================"
    echo -e "${GREEN}  TEST PASSED!${NC}"
    echo "========================================"
    echo ""
    print_status "Services are running correctly."
else
    echo "========================================"
    echo -e "${RED}  TEST FAILED${NC}"
    echo "========================================"
    echo ""
    print_error "API request did not return expected response."
    echo ""
    print_status "Service logs:"
    docker compose logs --tail=50
    exit 1
fi

# Print helpful commands
echo ""
echo "Useful commands:"
echo "  View logs:     docker compose logs -f"
echo "  Stop services: docker compose down"
echo "  Clean up:      docker compose down -v --rmi local"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:8080/v1/chat/completions \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"llama3.2:1b\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}'"
