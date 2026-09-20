build: clean
	go build -o application .
build-linux: clean
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o application .
run:
	go run main.go
test:
	go test -v ./...
clean:
	rm -f application

docker: build-linux
	docker build -t openpay .
	rm -f application

docker-run: docker
	@echo "Tagging image as latest"
	docker tag openpay openpay:latest
	@echo "removing existing container named openpay if any"
	docker rm -f openpay || true
	@echo "Running image with name openpay, mapping ports 8085:8085 and 8086:8086"
	docker run -d --name openpay -p 8085:8085 -p 8086:8086 openpay:latest

install: 
	go mod tidy
	go get github.com/grpc-ecosystem/grpc-gateway/v2/internal/descriptor@v2.27.2
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2
	go install google.golang.org/protobuf/cmd/protoc-gen-go
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	go install github.com/envoyproxy/protoc-gen-validate@latest
	go install github.com/gofreego/goutils/cmd/sql-migrator@v1.3.8

setup:
	@echo "Compiling proto files..."
	sh ./api/protoc.sh
	go mod tidy
