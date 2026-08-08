# Empieza por aquí: una fuente mínima completa

Esto compila tal cual. Cópialo y cámbialo — es más rápido que leer la
especificación entera, que viene después.

```json
{
  "version": 1,
  "project": "led_18650",
  "sheet": "auto",

  "blocks": [
    {
      "name": "led",
      "symbols": [
        { "ref": "BT1", "lib": "Device:Battery_Cell", "value": "18650" },
        { "ref": "R1", "lib": "Device:R", "value": "100", "rot": 90,
          "place": { "pin": "1", "at": "BT1.+", "dir": "up", "cells": 1 } },
        { "ref": "D1", "lib": "Device:LED", "value": "LED_RED", "rot": 90,
          "place": { "pin": "A", "at": "R1.2", "dir": "right", "cells": 3 } }
      ]
    }
  ],

  "nets": {
    "VBAT":   ["BT1.+", "R1.1"],
    "_ANODE": ["R1.2", "D1.A"],
    "GND":    ["D1.K", "BT1.-"]
  },

  "power_nets": { "GND": "power:GND" }
}
```

Lo que hay que tener claro de un vistazo, porque es donde todo el mundo se
equivoca la primera vez:

| Cosa | Es así | NO es así |
|---|---|---|
| Raíz | `"version": 1` es obligatorio | omitirlo |
| Componentes | `"symbols"` dentro de cada bloque | `"components"` |
| Nets | un MAPA `{ "NOMBRE": ["REF.pin", …] }` | una lista de objetos |
| Posición | anidada en `"place": {pin, at, dir, cells}` | `"x"`/`"y"` sueltos |
| Coordenadas | NUNCA se escriben: se ancla pin a pin | milímetros a mano |

El primer símbolo de cada bloque no lleva `place`: es el ancla del bloque. Cada
símbolo siguiente cuelga de un pin de otro **del mismo bloque**, ya declarado.

**Ancla por el pin que mira hacia donde vienes.** `"place": {"pin": "A", "at":
"R1.2", "dir": "right"}` dice *"el pin A cae a la derecha de R1.2"*, así que el
ánodo queda en el lado LEJANO y el cable tiene que rodear el componente para
alcanzarlo. Si la señal entra por la izquierda, ancla el pin que queda a la
izquierda (o gira con `rot`, o usa `"mirror": true`).

---

