CC      ?= gcc
CFLAGS  := -std=c11 -O3 -Wall -Wextra -Wpedantic -Wno-unused-parameter \
           -D_POSIX_C_SOURCE=200809L -pthread
LDFLAGS := -pthread -lm

# Use -march=native only for non-cross-compilation builds
NATIVE  ?= 1
ifeq ($(NATIVE),1)
    CFLAGS += -march=native
endif

TARGET  := embed-service
SRCS    := main.c embedder.c http.c json.c
OBJS    := $(SRCS:.c=.o)

.PHONY: all clean test bench size docker

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(OBJS) -o $@ $(LDFLAGS)

%.o: %.c
	$(CC) $(CFLAGS) -c $< -o $@

# Dependencies
main.o:     main.c embedder.h http.h json.h
embedder.o: embedder.c embedder.h
http.o:     http.c http.h
json.o:     json.c json.h

clean:
	rm -f $(OBJS) $(TARGET) *.dSYM *.gcda *.gcno *.gcov

# Quick smoke test: start server, hit endpoints, stop
test: $(TARGET)
	@echo "=== Starting embed-service for smoke test ==="
	@./$(TARGET) &
	@SERVER_PID=$$!; \
	sleep 1; \
	echo "--- Health check ---"; \
	curl -s http://localhost:8090/health | python3 -m json.tool; \
	echo "--- Embedding test ---"; \
	curl -s -X POST http://localhost:8090/v1/embeddings \
		-H "Content-Type: application/json" \
		-d '{"model":"local-embed","input":"hello world","encoding_format":"float"}' \
		| python3 -c "import sys,json; d=json.load(sys.stdin); e=d['data'][0]['embedding']; print(f'dims={len(e)}, first5={e[:5]}, norm={sum(x*x for x in e):.6f}')"; \
	echo "--- Determinism test ---"; \
	E1=$$(curl -s -X POST http://localhost:8090/v1/embeddings \
		-H "Content-Type: application/json" \
		-d '{"model":"local-embed","input":"test determinism","encoding_format":"float"}' \
		| python3 -c "import sys,json; print(json.load(sys.stdin)['data'][0]['embedding'][:3])"); \
	E2=$$(curl -s -X POST http://localhost:8090/v1/embeddings \
		-H "Content-Type: application/json" \
		-d '{"model":"local-embed","input":"test determinism","encoding_format":"float"}' \
		| python3 -c "import sys,json; print(json.load(sys.stdin)['data'][0]['embedding'][:3])"); \
	echo "  Run 1: $$E1"; \
	echo "  Run 2: $$E2"; \
	if [ "$$E1" = "$$E2" ]; then echo "  PASS: Deterministic"; else echo "  FAIL: Non-deterministic!"; fi; \
	kill $$SERVER_PID 2>/dev/null; \
	wait $$SERVER_PID 2>/dev/null; \
	echo "=== Smoke test complete ==="

# Benchmark: measure embeddings/second
bench: $(TARGET)
	@echo "=== Embedding Benchmark ==="
	@./$(TARGET) &
	@SERVER_PID=$$!; \
	sleep 1; \
	COUNT=500; \
	echo "Running $$COUNT embedding requests..."; \
	START=$$(python3 -c "import time; print(time.time())"); \
	for i in $$(seq 1 $$COUNT); do \
		curl -s -o /dev/null -X POST http://localhost:8090/v1/embeddings \
			-H "Content-Type: application/json" \
			-d '{"model":"local-embed","input":"benchmark sentence number '$$i'","encoding_format":"float"}'; \
	done; \
	END=$$(python3 -c "import time; print(time.time())"); \
	python3 -c "s=$$START; e=$$END; d=e-s; print(f'  Requests: $$COUNT'); print(f'  Duration: {d:.2f}s'); print(f'  Throughput: {$$COUNT/d:.1f} embeddings/sec')"; \
	kill $$SERVER_PID 2>/dev/null; \
	wait $$SERVER_PID 2>/dev/null; \
	echo "=== Benchmark complete ==="

# Show compiled binary size
size: $(TARGET)
	@echo "=== Binary Size ==="
	@ls -lh $(TARGET) | awk '{print $$5 "  " $$NF}'
	@echo "--- Section breakdown (if available) ---"
	@size $(TARGET) 2>/dev/null || echo "(size command not available)"

# Build the Docker image
docker:
	docker build -t stilltent/embed-service:latest .
