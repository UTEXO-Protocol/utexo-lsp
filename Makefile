help:
	@echo "Usage:"
	@echo ""
	@fgrep -h "##" $(MAKEFILE_LIST) | fgrep -v fgrep | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

up: ## Start the application
	docker compose up -d

down: ## Stop the application
	docker compose down -v --rmi=local --remove-orphans
