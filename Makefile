.PHONY: up up-voice down logs ps config smoke seed topics dbt trino eval clean

up:            ## Full platform (middleware + services + web)
	docker compose up -d --build

up-voice:      ## Platform + local voice models (ollama/piper)
	docker compose --profile voice up -d --build

down:          ## Stop everything
	docker compose down

clean:         ## Stop and delete volumes (DESTRUCTIVE)
	docker compose down -v

logs:          ## Tail all logs
	docker compose logs -f --tail=200

ps:
	docker compose ps

config:        ## Validate merged compose config
	docker compose config -q && echo "compose OK"

topics:        ## Re-run Kafka topic init
	docker compose run --rm kafka-topics

seed:          ## Seed demo tenant "acme" + catalog + availability
	./scripts/seed-demo.sh

smoke:         ## End-to-end smoke test through APISIX gateway
	./scripts/smoke-test.sh

dbt:           ## Run lakehouse dbt marts (silver+gold)
	docker run --rm --network opendesk -v $$PWD/infra/lakehouse/dbt:/usr/app ghcr.io/dbt-labs/dbt-trino:1.8.x build

trino:         ## Open a trino CLI against the lakehouse
	docker exec -it trino trino --catalog iceberg --schema gold

eval:          ## Voice agent eval harness (scenarios -> /voice/chat + LLM judge)
	python3 services/voice-agent-runtime/eval/eval.py $(EVAL_ARGS)
