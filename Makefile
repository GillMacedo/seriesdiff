.PHONY: test lint demo scrapes

RUN = go run ./plugin/scripts

test:
	test -z "$$(gofmt -l plugin tests examples)"
	go vet ./...
	go test ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

demo:
	$(RUN) compare --refs examples/monitoring examples/scrapes/orders-before.prom examples/scrapes/orders-after.prom
	$(RUN) compare examples/scrapes/orders-before.prom examples/scrapes/orders-comments.prom

# Rebuilds the example scrapes from examples/orders. Needs curl, and the
# network the first time to download client_golang.
scrapes:
	scripts/scrape-examples.sh
