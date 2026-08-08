# Formato fuente `.design.json` — especificación v1

Principio: **el `.kicad_sch` es un artefacto compilado; nadie lo edita a mano.**
El LLM escribe y edita este fichero fuente; `compile_schematic` lo convierte en
esquemático determinista (misma fuente → mismo fichero byte a byte, UUIDs por
hash de referencia) y devuelve en una llamada: PNG renderizado, informe ERC,
métricas y el informe de decisiones del compilador.

## Claves de nivel superior

| Clave | Significado |
|---|---|
| `version` | Versión del formato (1). |
| `project` | Nombre del proyecto; da nombre al `.kicad_sch`. |
| `sheet` | `"A4"`, `"A3"` o `"auto"` (elige la menor hoja donde cabe el contenido con márgenes de 12.7 mm). |
| `blocks` | Bloques funcionales (ver abajo). Unidad de pensamiento = bloque de datasheet. |
| `arrange` | Filas de bloques, arriba→abajo, cada fila izquierda→derecha. |
| `nets` | Netlist global: nombre → lista de pines `REF.pin` (número o nombre). Un nombre que empieza por `_` es un net "silencioso": solo conectividad, sin etiqueta impresa cuando el cable ya lo une todo (nudos internos tipo R→LED). Reserva los nombres visibles para señales con significado. |
| `power_nets` | Nets que son rails: nombre → `lib_id` del símbolo power. Nunca se rutean: un símbolo power por pin (política per-pin existente). |
| `no_connect` | Lista de pines `REF.pin`, o el literal `"unused"` por referencia: todo pin no usado en `nets` ni plantillas recibe `no_connect`. El informe de compilación enumera cuáles fueron — revisar siempre esa lista. |

## Bloques

Dos clases, distinguidas por la presencia de `template`:

**Bloque plantilla** — instancia una plantilla cableada de `place2/templates`
(geometría y wires horneados, ya verificados):

```json
{ "name": "power", "template": "voltage_regulator_linear",
  "refs":    { "REG": "U2", "C_IN_BYP": "C4" },
  "connect": { "VIN": "VIN", "VOUT": "+5V" } }
```

- `refs`: rol de la plantilla → referencia real. Roles sin mapear reciben referencia automática.
- `connect`: pin externo de la plantilla (su etiqueta pública) → net global.

**Bloque explícito** — símbolos colocados por árbol de anclajes:

```json
{ "ref": "C1", "lib": "Device:C", "value": "100n",
  "place": { "pin": "1", "at": "U1.VCC", "dir": "left", "cells": 4 } }
```

Semántica: *el pin `pin` de este símbolo cae exactamente a `cells` celdas de
rejilla (2.54 mm) en dirección `dir` desde el pin `at`.* El compilador calcula
el origen del símbolo con la geometría real de pines (`pininfo`); el autor
jamás escribe milímetros ni coordenadas absolutas.

- El primer símbolo del bloque es el ancla: se coloca en el origen del bloque, sin `place`.
- `at` solo puede referirse a un símbolo del mismo bloque ya declarado antes
  (la colocación es un **árbol**, evaluado en orden: no hay solver, no puede fallar).
- `dir`: `left` | `right` | `up` | `down`. `cells`: entero ≥ 1.
- `rot` (0/90/180/270) opcional; sin `rot` se usa 0 — las orientaciones de
  librería de KiCad ya son las canónicas (R y C verticales con pin 1 arriba,
  crystal horizontal). `mirror` está reservado en el formato pero el compilador
  aún no lo soporta (error explícito).
- Un pin con nombre repetido (pines apilados, p. ej. `U1.VCC` en 4 y 6) resuelve
  al de menor número de pin, determinista.
- **Multi-unidad**: una parte con varias unidades (NE5532 A/B/C) se declara con
  un símbolo por unidad — misma `ref`, misma `lib`, `unit` distinto (0/ausente
  = unidad 1). En `nets` y en `place.at` el pin se califica `"REF.unidad.pin"`
  (`"U1.2.6"` = unidad 2, pin 6); con una sola unidad valen ambas formas.
  Repetir el par (ref, unit) o cambiar la `lib` entre unidades es error.

## `arrange`

Cada nombre debe corresponder a un bloque declarado y no puede repetirse
(error de validación). Los bloques ausentes de `arrange` van cada uno a su
propia fila al final, en orden de declaración.

El compilador calcula el bbox real de cada bloque y coloca las filas con margen
mínimo de 4 celdas entre bloques y entre filas. **El solape entre bloques es
imposible por construcción.** El solape *dentro* de un bloque (dos anclajes que
chocan) es error de compilación con las dos referencias implicadas — nunca se
emite un esquemático con símbolos superpuestos.

## Cableado — el autor nunca dibuja cables

Política (la que ya demostró funcionar): wires literales de las plantillas +
fórmulas cerradas de `wiregen` para clústeres triviales + **etiquetas para todo
lo demás**. `power_nets` emiten un símbolo por pin. El `gate` corre siempre al
final: 0 cruces entre nets, 0 cables a través de cuerpos, 0 solapes colineales,
o la net se degrada a etiquetas. El informe dice qué degradó y por qué.

## Salida de `compile_schematic`

1. `<project>.kicad_sch` (determinista) + PNG renderizado.
2. Informe ERC (kicad-cli, con `-o` a fichero).
3. Métricas (`place2/metrics`).
4. Informe de decisiones: net → wire/label y motivo, pines auto-`no_connect`,
   bboxes de bloques, hoja elegida.

Bucle de trabajo: compilar → mirar PNG e informes → editar la fuente (mover un
símbolo = cambiar `cells` o `dir`) → recompilar. La fuente vive junto al
proyecto y es el único artefacto que se edita.

## Invariantes garantizados por construcción

- Todos los pines en rejilla de 2.54 mm (el autor no puede sacarlos de rejilla).
- Cero solapes de símbolos (inter-bloque por `arrange`; intra-bloque por error de compilación).
- Cero cruces entre nets, cero cables por cuerpos, cero solapes colineales (gate).
- Un símbolo power por pin en rails; nunca ruteados.
- Determinismo total: misma fuente → mismo `.kicad_sch` byte a byte.
