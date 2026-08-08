

# motel

[![CI](https://github.com/andrewh/motel/actions/workflows/ci.yml/badge.svg)](https://github.com/andrewh/motel/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/andrewh/motel)](https://goreportcard.com/report/github.com/andrewh/motel)
[![Go Reference](https://pkg.go.dev/badge/github.com/andrewh/motel.svg)](https://pkg.go.dev/github.com/andrewh/motel)

> **motel** /mōˈtel/ _sustantivo_
> opentelemetry simulada (mock). Un generador de señales sintéticas para probar
> y desarrollar pipelines de observabilidad.

`motel` es un generador sintético de [OpenTelemetry](https://opentelemetry.io/).

Describe tu sistema distribuido en YAML y motel generará trazas realistas,
métricas y registros (logs), sin necesidad de servicios en vivo.

¿Ya usas `telemetrygen`? Consulta [cómo se compara motel](#motel-vs-telemetrygen).

## Instalación

```sh
brew tap andrewh/tap
brew install motel
```

O con Go:

```sh
go install github.com/andrewh/motel/cmd/motel@latest
```

O descarga un binario desde la [página de releases](https://github.com/andrewh/motel/releases).

## Inicio rápido

```yaml
# my-topology.yaml
version: 1

services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 1%
        calls:
          - users.list
  users:
    operations:
      list:
        duration: 15ms +/- 5ms

traffic:
  rate: 10/s
```

```sh
# Validar la topología
motel validate my-topology.yaml

# Generar trazas a stdout
motel run --stdout --duration 5s my-topology.yaml

# Enviar a un colector OTLP
motel run --endpoint localhost:4318 --duration 30s my-topology.yaml
```

## Qué hace

motel lee un archivo de topología YAML que describe servicios, operaciones, patrones
de llamadas, distribuciones de latencia y tasas de error. Recorre el árbol de topología
una vez por traza, produciendo spans que parecen provenir de servicios instrumentados
reales. Cada span lleva los atributos `synth.service` y `synth.operation`,
y todas las señales incluyen un atributo de recurso `motel.version`, por lo que el tráfico
sintético nunca se confunde con datos reales.

Casos de uso:

- **Probar pipelines de observabilidad** — alimenta colectores,
  backends o paneles con trazas realistas sin desplegar servicios
- **Pruebas de carga** — genera tráfico de trazas a tasas controladas con patrones
  configurables (uniforme, diurno, en ráfaga, personalizado)
- **Demostraciones y prototipos** — muestra cómo se verá la telemetría de tu sistema
  antes de construirlo
- **Importar trazas reales** — `motel import` infiere una topología a partir de datos de
  trazas existentes, para que puedas reproducir y modificar patrones de producción

## motel vs telemetrygen

[telemetrygen](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/cmd/telemetrygen)
es el generador integrado del OpenTelemetry Collector, y es la herramienta adecuada
para verificar que la conexión funciona: emite spans idénticos a una
tasa configurable. motel existe para responder preguntas que telemetrygen no puede —
si tu pipeline se comporta *correctamente* cuando la telemetría se parece a
la de producción.

|                      | telemetrygen                       | motel |
|----------------------|------------------------------------|-------|
| Forma de traza       | spans planos idénticos             | topología arbitraria de servicios, llamadas en paralelo/secuenciales |
| Latencia             | duración fija del span             | distribuciones por operación (`30ms +/- 10ms`) |
| Errores              | un código de estado para cada span | tasas de error por operación |
| Modos de fallo       | —                                  | escenarios por ventana de tiempo, backpressure, circuit breakers, rechazo de cola |
| Tráfico              | tasa constante                     | patrones uniforme, diurno, en ráfaga, personalizado |
| Métricas y registros | generados de forma independiente   | correlacionados — derivados de la misma topología que las trazas |
| Convenciones semánticas | atributos genéricos              | los dominios semconv generan atributos estándar por operación |
| Reproducción de prod | —                                  | `motel import` infiere una topología a partir de trazas reales |

Regla general: usa telemetrygen para probar la conectividad y la capacidad de
procesamiento bruto. Usa motel cuando la corrección dependa del *contenido* de la
telemetría — [muestreo de cola](docs/how-to/test-tail-sampling.md),
[transformaciones OTTL](docs/how-to/test-ottl-transforms.md),
[umbrales de alerta](docs/how-to/test-alert-thresholds.md),
[integridad del muestreo](docs/how-to/test-sampling-integrity.md), paneles,
y demostraciones.

## Señales

Por defecto, motel emite trazas. Usa `--signals` para agregar métricas y registros (logs):

```sh
motel run --stdout --signals traces,metrics,logs --slow-threshold 200ms topology.yaml
```

Los tres tipos de señales son impulsados por la misma topología.

## Documentación

- [Tutorial de inicio](docs/tutorials/getting-started.md)
- [Referencia de CLI](docs/reference/synth.md)
- [Referencia de DSL](cmd/motel/README.md) — esquema completo de topología
- [Topologías de ejemplo](docs/examples/README.md)
- [Stack de Compose para seguir el tutorial](docs/examples/compose/README.md) — colector con muestreo de cola + Jaeger, listo con un `docker compose up`
- [Modelado de tus servicios](docs/how-to/model-your-services.md)
- [Cómo import infiere una topología](docs/explanation/import-pipeline/README.md)
- [Cómo motel utiliza las convenciones semánticas de OTel](docs/explanation/semantic-conventions.md)

## Licencia

[Apache 2.0](LICENSE)
