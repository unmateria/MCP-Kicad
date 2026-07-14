# MCP-KiCad — pipeline arquitectural

## Vista general

El servidor expone tools MCP que el LLM invoca para diseñar PCBs. Cada
modificación de schematic pasa por:

```
parse (.kicad_sch → AST) → mutate (sexp.Schematic) → serialize → write
```

Las dos rutas críticas que tocan layout son **`relayout`** y **`connect_netlist`**.

## Pipeline (`internal/place2/`)

```
┌────────────┐  ┌──────────────┐  ┌────────────┐  ┌────────┐
│ Capture    │→ │ Cluster.Detect│→│ ELK layout │→│ Snap   │
└────────────┘  └──────────────┘  └────────────┘  └────────┘
       ↓                ↓                ↓             ↓
  intent nets     core + canonical   Sugiyama      grid 1.27
                  detectors          (subprocess)
                                     fallback A*
       ↓
┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────┐
│ Rules      │→ │ PowerPlacer│→│ Route       │→│ Verify │
└────────────┘  └────────────┘  └────────────┘  └────────┘
   power top    one #PWR per     route2.Steiner   ERC clean
   GND bottom   (rail, snapPos)  + A*++
   signal L→R   stub 2.54mm
   R/C rotation
```

> **Estado actual.** Los pasos PowerPlacer (P1), Steiner (P2) y los detectores
> canónicos (P3) viven ya en producción a través de `tools/netlist.go` y
> `tools/schematic.go::relayout`. El `place2.Pipeline.Run` sigue siendo la
> fachada de futuro — el cutover completo está en backlog.

## Paquetes nuevos

| Paquete | Responsabilidad |
|---|---|
| `internal/place2/power` | Cálculo unificado de offset, dedup y bus-alignment de #PWR. |
| `internal/place2/cluster/canonical` | Detectores extra (bypass_nonpower, series_led, oscillator_rc, feedback_divider). Se autoregistran vía `init()`. |
| `internal/place2/templates` (Stamp) | Estampar templates JSON en una posición arbitraria. |
| `internal/route2` (steiner.go, collinear.go) | Steiner trunk rectilineal y agrupación por colinearidad. |
| `internal/testutil` | Normalizador y comparador de schematics para goldens. |

## Comandos canarios

- `cmd/verify_e2e` — LED simple + NE5532 multi-unit + NE555 astable; asserts ERC OK + cero duplicados de power.
- `cmd/update_goldens` — regenera `testdata/golden/<demo>.{kicad_sch,metrics.json}` tras cambios deliberados.
- `cmd/measure_layout` — métricas para un schematic concreto.
- `cmd/demo_apply_template_opamp` — comprueba que `apply_template` deja el opamp_noninverting con ERC limpio.
