# Routing — `internal/route2/` y `internal/router/`

## Estado

- **Producción:** `internal/router/` (A* legacy) + Steiner pre-pass de
  `internal/route2/`.
- **Fallback:** `internal/route2/` (`astarpp`) — usado por el optimizador.
- **Pendiente cutover total:** sustituir todas las llamadas a
  `router.NewRouter` por `route2.New` en `tools/schematic.go` y
  `tools/netlist.go`.

## Steiner pre-pass

`internal/route2/steiner.go` calcula un Steiner rectilineal cuando un net tiene
≥4 pines colineales (mismo X o mismo Y dentro de 1.27 mm) y al menos el 75% de
los pines del net están en el mismo eje. Genera UN trunk + stubs perpendiculares
en lugar del MST típico.

```
pins (X=50,Y=10) (X=50,Y=20) (X=50,Y=30) (X=50,Y=40)   ← colineales en X=50
                       │
                       │  trunk (X=50, Y=10..40)
                       │
       no stubs porque todos los pines ya están en el trunk
```

## A*++ (`route2/astarpp.go`)

- `bendPenalty = 14` (legacy: 8) — fuerza tramos rectos
- `wireCrossCost = 50` (legacy: 20) — penaliza cruces
- `maxExpanded = 80000` — más nodos para casos densos
- Heurística angular + cross-prevention contra wires de otros nets ya rutadas.

## Decisiones por defecto

| Caso | Estrategia |
|---|---|
| 2 pines | wire L-routing (smart-L) |
| 3-pin no-power | MST greedy + A* legacy |
| ≥4 pines colineales | Steiner trunk + stubs |
| Power net (GND/VCC/+5V/...) | `add_power_rail` por pin via `power.Emitter` |
| Imposible rutar | label fallback |
