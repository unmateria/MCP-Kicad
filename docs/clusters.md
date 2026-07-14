# Cluster catalog — `internal/place2/cluster/`

## Reglas core (rules.go)

| Kind | Disparador | Anchor |
|---|---|---|
| `decoupling` | C entre rail y GND con un IC tocando ambos rails | IC (REF#unit) |
| `pullup` | R en señal-pin de IC con extremo en VCC/+5V | IC |
| `lc_filter` | L+C en cadena de regulador | IC regulador |
| `crystal` | XTAL + dos load caps | IC |
| `opamp_feedback` | Rf+Rin alrededor del op-amp | Op-amp |
| `bias_compensation` | R + C entre + del op-amp y GND | Op-amp |
| `voltage_divider` | Dos R en serie entre dos rails | R alta |
| `io_connector` / `io_input` / `io_output` | Connector con net VIN / VOUT | Connector |
| `header` | Connector_Generic con pines a un mismo IC | Connector |

## Reglas canónicas (canonical/)

| Kind | Disparador | Confianza |
|---|---|---|
| `bypass_nonpower` | Cap entre rail y pin no-power (BYP/CTL/FB/COMP/REF/SS/BS/BOOT) de un IC | 0.85 |
| `series_led` | R+LED en serie sobre un mismo net | 0.80 |
| `oscillator_rc` | IC con pin TRIG/THR/DSC/OSC/CLK + R+C en su net | 0.90 |
| `feedback_divider` | Dos R sobre el net del pin FB/ADJ de un IC | 0.85 |

## Cómo añadir un detector

1. Crear `cluster/canonical/<kind>.go` con la firma:
   ```go
   func myDetector(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster
   ```
2. Registrarlo en `canonical.All()` con su priority (los priorities mayores corren después).
3. Añadir un caso para `<kind>` en `internal/place2/clusterapply.go::bboxAwareOffsetsCtx` para que los satélites tengan posición port-direction-aware.
4. Añadir un test fixture en `cluster/canonical/canonical_test.go`.

## Confidence

`Cluster.Confidence ∈ [0,1]`. La detección core asigna 0 (cae al orden de
prioridad). Los detectores canónicos publican confianzas explícitas para que
en el futuro un colisionador resuelva conflictos por confianza en lugar de por
orden de inserción.
