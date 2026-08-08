# Routing — `internal/route2/` y `internal/router/`

## Estado

- **Producción:** `internal/router/` (A* legacy) + Steiner pre-pass de
  `internal/route2/`.
- `internal/route2/` (`astarpp`) ya NO tiene consumidor propio: el optimizador
  que lo usaba se retiró en F4 (commit 7860262). De route2 solo vive el
  pre-paso Steiner, invocado desde `tools/steiner_helper.go`.
- **Pendiente cutover total:** sustituir todas las llamadas a
  `router.NewRouter` por `route2.New` en `tools/schematic.go` y
  `tools/netlist.go`.

## Regla intocable: ningún cable toca un pin de otro net

En KiCad, tocar la punta de un pin ES conexión. Un cable que termina en —o
cruza— un pin ajeno fusiona dos nets, y después **ya no hay quien lo detecte**:
queda un solo net consistente, sin cruces, y la puerta geométrica no tiene nada
que objetar. Hay que impedirlo al dibujar:

- **A\*:** `router.RouteAvoiding(x1,y1,x2,y2, avoid)` bloquea las puntas de los
  demás nets durante la llamada y las restaura al salir.
- **Stubs forzados:** `routeWithExits` *afirma* los primeros y últimos 2.54 mm
  en la dirección del pin, saltándose la búsqueda; si un stub caería sobre un
  pin ajeno, se descarta (entrar de frente es un detalle estético, un
  cortocircuito no).
- **Weld:** `touchesForeignPin` antes de `commit`.
- **Símbolos de power:** `power.ComputeClear` se aleja hasta 3 celdas.

Los 6 cortocircuitos que el property test destapaba eran esta única regla
ausente en esos sitios.

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
