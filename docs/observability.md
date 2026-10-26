# Проверка observability в окружении курса (ЛР2)

Курс: `tripgoctl v0.1.2`, `environment.toml` с `lab = 2` и компонентами `postgres`, `observability`. Во время проверки поды PostgreSQL, OTel Collector, Jaeger, Prometheus, Grafana и Loki имели статус Ready. После запуска сервиса с `.env.example` и созданным `tripgoctl` файлом `.env` выполнены HTTP-сценарии и повторный `make test-integration` с `TRIPGO_BASE_URL`.

## Трейс запроса

[Jaeger — POST /api/v1/trips и вложенные DB-спаны](<screenshots/jaeger-create-trace.png>). [Раскрытый DB-спан](<screenshots/jaeger-db-attributes.png>) подтверждает `db.operation.name=INSERT` и `db.collection.name=trips` без значений SQL. Один реальный запрос вернул `201`, а Jaeger показал 6 спанов, включая `BEGIN`, вставку в `trips`, вставку в `trip_status_history` и `COMMIT`. Полный trace ID: `138897618cae2a35815532e7c17bc98c` (локально: `http://localhost:22686/trace/138897618cae2a35815532e7c17bc98c`). Соответствующие строки stdout:

```json
{"level":"INFO","msg":"trip created","service":"trip-service","trace_id":"138897618cae2a35815532e7c17bc98c","span_id":"68cb13bc305d6b67"}
{"level":"INFO","msg":"http request","service":"trip-service","http.request.method":"POST","http.route":"/api/v1/trips","http.response.status_code":201,"duration_ms":1.636123,"trace_id":"138897618cae2a35815532e7c17bc98c","span_id":"68cb13bc305d6b67"}
```

В stdout исходных записей есть также поле `time`; здесь оно опущено для краткости. Персональные ID и тело запроса в события не включены. Отдельные GET/finish-трейсы с UUID в пути проверены через Jaeger API: этот UUID не встретился ни в именах, ни в атрибутах, ни в событиях экспортированных спанов. Привязка выполняется по точному `trace_id`, а не по времени или имени операции.

## Метрики

- [Grafana — `tripgo_trips_created_total`](<screenshots/grafana-trips-created.png>): счётчик вырос после последовательных операций создания.
- [Grafana — средняя длительность POST `/api/v1/trips`](<screenshots/grafana-http-duration.png>): запрос `http_server_request_duration_seconds_sum{http_route="/api/v1/trips"} / http_server_request_duration_seconds_count{http_route="/api/v1/trips"}` показывает отдельные ряды для HTTP `201` и `409` с шаблоном маршрута, а не UUID.

Оба графика отображались в Grafana Explore через подключённый Prometheus; проверки `/health`, `/ready`, PostgreSQL integration и конкурентные create/finish были выполнены в том же окружении.
