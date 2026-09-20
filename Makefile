build: clean
	go build -o application .
build-linux: clean
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o application .
run:
	go run main.go
test:
	go test -v ./...

# Integration tests need a real Postgres: they exercise row locking, SKIP
# LOCKED and unique constraints under contention, none of which a fake can
# check. They run against openpay_test and TRUNCATE as they go, so they must
# never be pointed at a database anyone cares about.
test-integration:
	docker compose up -d postgres
	@until docker compose exec -T postgres pg_isready -U openpay -d openpay_test >/dev/null 2>&1; do sleep 1; done
	@# sql-migrator waits for a signal after finishing ("press ctrl+c to exit"),
	@# so run it in the background and stop it once the migration is applied.
	@sql-migrator ./migrator.test.yaml & MIG=$$!; sleep 5; kill $$MIG 2>/dev/null || true
	OPENPAY_TEST_POSTGRES=1 go test -race ./...

migrate:
	@sql-migrator ./migrator.yaml & MIG=$$!; sleep 5; kill $$MIG 2>/dev/null || true

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
