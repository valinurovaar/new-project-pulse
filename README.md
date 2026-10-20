# ProjectPulse

ProjectPulse — HTTP API на Go для асинхронного мониторинга пользовательских веб-проектов. Сервис хранит состояние в PostgreSQL, отправляет задачи доверенному сервису диагностики и транзакционно ведёт баланс диагностических кредитов.

Полное исходное задание сохранено в [SPECIFICATION.md](SPECIFICATION.md).

## Что реализовано

- регистрация и вход, Bearer JWT (HMAC-SHA256), salted PBKDF2-хеши паролей;
- проекты, архивирование, история BASIC-проверок и платных аудитов;
- проверка/нормализация URL с DNS-разрешением и блокировкой loopback, private, link-local и multicast адресов;
- PostgreSQL-миграции, UUID, ограничения и частичные уникальные индексы;
- атомарная выдача 30 стартовых кредитов и списание 10/20/15 кредитов;
- награда 5 кредитов за HEALTHY: не чаще раза в UTC-сутки на проект и максимум 20 в сутки на пользователя;
- идемпотентное завершение, начисление и возврат платного аудита;
- устойчивые фоновые задания через PostgreSQL (`FOR UPDATE SKIP LOCKED`), polling, тайм-ауты, `Retry-After`, backoff и продолжение после рестарта;
- автоматические BASIC-проверки с настраиваемым интервалом;
- единый JSON-формат ошибок и graceful shutdown;
- unit- и HTTP-тесты; покрытие пакетов бизнес-правил и API превышает 60%.

## Быстрый запуск

Нужны Go 1.25+ и PostgreSQL 14+.

```bash
cp .env.example .env
# задайте AUTH_SECRET длиной не менее 32 символов
go run ./cmd/projectpulse
```

Приложение автоматически применяет встроенные миграции при старте. Значения окружения имеют приоритет над флагами:

```text
RUN_ADDRESS       -a   :8080
DATABASE_URI      -d   postgres://...
INSPECTOR_ADDRESS -i   http://localhost:8081
AUTH_SECRET       -s   (обязателен, >= 32 символов)
CHECK_INTERVAL    -c   24h
WORKER_COUNT      -w   2
```

Для запуска PostgreSQL и API в контейнерах:

```bash
docker compose up --build
```

Адрес внешнего диагностического сервиса передаётся через `INSPECTOR_ADDRESS`. Его контракт соответствует разделам 22–23 ТЗ; сам диагностический сервис намеренно не входит в приложение.

ProjectPulse проверяет DNS при регистрации и повторно непосредственно перед отправкой задания. Доверенный диагностический сервис также обязан отклонять private, loopback, link-local и иные служебные адреса после собственного DNS-разрешения и на каждом redirect — это необходимая часть SSRF-границы между сервисами.

## Тесты

```bash
go test ./... -race -cover
go test ./internal/api ./internal/auth ./internal/config ./internal/domain ./internal/urlcheck ./internal/worker -coverprofile=coverage.out
go tool cover -func=coverage.out
```

При наличии отдельной тестовой PostgreSQL задайте `TEST_DATABASE_URI`: тогда автоматически выполняются интеграционные сценарии конкурентных начислений, единственного refund, дневного лимита и гонки архивации с созданием проверки. GitHub Actions запускает этот набор с PostgreSQL 17.

Тесты PostgreSQL-адаптера можно дополнять интеграционным набором против одноразовой базы. Критические гарантии дополнительно обеспечены транзакциями, блокировками строк и уникальными индексами миграции.

## Архитектура

```text
cmd/projectpulse       запуск, сигналы и graceful shutdown
internal/api           HTTP API, auth middleware, JSON DTO/errors
internal/auth          пароли и access tokens
internal/domain        сущности и чистые бизнес-правила
internal/postgres      PostgreSQL repository и транзакции кредитов
internal/urlcheck      защита публичных target URL
internal/worker        отправка/polling задач и scheduler
migrations             встроенные SQL-миграции
```

Очередь хранится в таблице `checks`; поэтому потеря процесса не теряет задания. Выборка просроченных задач выдаёт короткую lease через `next_attempt_at`, а конкурентные воркеры используют `SKIP LOCKED`. Финальные операции сначала блокируют строку проверки, проверяют финальный статус и только затем в одной транзакции изменяют проект/баланс/журнал.

## Пример

```bash
curl -s http://localhost:8080/api/v1/users/register \
  -H 'Content-Type: application/json' \
  -d '{"login":"developer","password":"strong-password"}'

curl -s http://localhost:8080/api/v1/projects \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"My Portfolio","url":"https://example.com"}'
```

Все маршруты находятся под `/api/v1` и соответствуют сводному API в ТЗ.
