# Optimizer — `internal/optimize/`

## Modelo

`Cost(layout) → CostBreakdown` puntúa una propuesta. El optimizador busca
el mínimo dentro de un presupuesto (`budget`) generando candidatos con
`Variator`s.

```
base layout → variator → candidate → materialize → cost → top-K / Pareto
```

## Variators disponibles

| Variator | Genera | Cuándo usarlo |
|---|---|---|
| `RotationVariator` | Producto cartesiano de rotaciones por ref | R/C/L simétricos |
| `SwapVariator` | Permutaciones dentro de la misma columna | Refinamiento tras rotación |
| `ChainVariator` | Concatena varios | Pipeline de búsqueda multi-fase |

## Búsqueda

| Función | Devuelve |
|---|---|
| `Search` | mejor único + breakdown |
| `SearchTopK` | k mejores ordenados por Cost.Total |
| `SearchPareto` | frontera no-dominada en (Crossings, BodyHits, WireLength) ordenada lexicográficamente |

## Tuning actual de `relayout`

- `budget = 64` (antes 8)
- Hasta 6 refs simétricos en el RotationVariator (antes 4)
- Encadenado: `Rotation → Swap`
- Mejora mínima requerida: 5% sobre el coste base (si no, se descarta)

## Componentes de coste

```
WireLength         × 0.05
WireCrossings      × 200    ← penalización dura
WireBodyHits       × 500    ← penalización dura
BodyOverlaps       × 5000   ← catastrófico
LabelHits          × 8
GridMisalign       × 100
PinAxisMisalign    × 80
WireBodyClear      × 100
WhitespaceVar      × 2
AxisAlignBonus     × −25    ← bonus
SymmetryDev        × 20
```
