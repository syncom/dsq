BINARY := dsq

.PHONY: build test run clean

# CGO disabled => fully statically linked executable with no runtime dependencies.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o $(BINARY) .

test:
	go test -race ./...

run: build
	QUESTIONS_FILE=questions.example.json STORAGE_DIR=data ./$(BINARY)

clean:
	rm -f $(BINARY)
