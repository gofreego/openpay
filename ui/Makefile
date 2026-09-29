.PHONY: install build dev lint typecheck test check clean generate-proto

install:
	npm install

build:
	npm run build

dev:
	npm run dev

lint:
	npm run lint

typecheck:
	npm run typecheck

test:
	npm test

# What CI runs.
check: typecheck lint test build

clean:
	rm -rf dist/assets dist/index.html node_modules

generate-proto:
	npm run generate:proto
