# MCP-KiCad

**Diseña esquemas electrónicos y placas hablando con Claude.** Es un servidor [MCP](https://modelcontextprotocol.io) que permite a un modelo de lenguaje dibujar esquemas de KiCad de verdad — colocando componentes, cableando nets, añadiendo símbolos de alimentación y pasando el chequeo eléctrico del propio KiCad —, convertirlos en placas de dos caras colocadas y ruteadas, y devolverte los ficheros para abrir, editar y mandar a fabricar.

*[Read me in English](README.md)*

<p align="center">
  <img src="docs/images/demo_buck_converter.svg" alt="Esquema de un convertidor reductor LM2596 generado por MCP-KiCad" width="620">
</p>

<p align="center"><em>Un convertidor reductor con LM2596. Nadie lo dibujó a mano: se compiló a partir de una descripción de texto, y pasa el ERC.</em></p>

---

## Qué hace exactamente

Le pides un circuito en lenguaje normal. Claude escribe una descripción declarativa corta y llama a una sola herramienta, `compile_schematic`, que convierte esa descripción en un esquema terminado.

> **Tú:** Hazme un astable con un 555 a 1 Hz aproximadamente, alimentado a 5 V.
>
> **Claude:** *[consulta los nombres reales de los pines del NE555, escribe el diseño y lo compila]*
> Listo — 7 componentes, ERC limpio. Aquí tienes la vista previa.

El resultado se abre en KiCad como cualquier otro esquema. No es un dibujo: es un fichero real con una netlist real, y con una llamada más se convierte en placa.

### Por qué el resultado sirve

Generar *algo* es fácil; generar un esquema que un humano acepte, no. Cada esquema compilado se comprueba antes de que lo veas, y el compilador se niega a emitir nada que falle:

| Garantía | Cómo se impone |
|---|---|
| Ningún cable cruza otro de una net distinta | Una barrera geométrica revisa cada segmento. Los infractores pasan a ser etiquetas de net |
| Ningún cable atraviesa el cuerpo de un componente | La misma barrera, contra el contorno real de cada símbolo |
| Todas las conexiones declaradas existen | El fichero terminado se vuelve a leer y su netlist se compara con lo que pediste |
| No hay conexiones de más | La misma comprobación al revés: un cable que toca un pin que no debe es un error |
| Los símbolos de alimentación tocan sus pines de verdad | Se verifica por contacto físico, no por nombre de net |
| Un punto de soldadura donde se juntan los cables | La regla se midió sobre los 115 esquemas que distribuye KiCad: punto donde se juntan tres o más extremos de cable, y donde se juntan dos sobre un pin |
| KiCad está de acuerdo | `kicad-cli` ejecuta el ERC sobre el resultado y el informe viene incluido |

Si una conexión no se puede dibujar limpia, degrada a etiqueta de net en vez de producir un cable que miente. **El esquema nunca está mal en silencio.**

Que una net se cruce *consigo misma* no es uno de esos casos: sigue siendo una sola net, así que el cable se corta en el cruce y se le pone un punto de soldadura en vez de tirarlo. Solo un conflicto real con otra net te cuesta un cable.

### Del esquema a la placa

Cada pieza sale de `compile_schematic` con un footprint comprobado: que existe y que tiene un pad para cada pin. Es el que indicaste, el que trae el símbolo de la librería de KiCad, o uno por defecto que se te dice. Después `compile_pcb` construye la placa:

<p align="center">
  <img src="docs/images/ne555_astable_pcb.svg" alt="Placa del astable con 555 colocada y ruteada por MCP-KiCad" width="520">
</p>

<p align="center"><em>El astable con 555 tal como lo coloca <code>compile_pcb</code>: piezas colocadas a partir del esquema, conectores en los bordes, pistas por las dos caras. Un plano de masa ocupa la cara de abajo (no se dibuja, para que se vean las pistas).</em></p>

| | |
|---|---|
| **Conexiones** | Se leen de la netlist que exporta el propio KiCad, así que la placa coincide con su *Actualizar PCB desde esquemático*, que sigue funcionando sobre ella |
| **Colocación** | Parte de la disposición del esquema; sin solapes de huellas; condensadores de desacoplo pegados a la patilla de alimentación de su chip, conectores en el borde, giros que acortan las conexiones |
| **Ruteo** | Dos caras, pistas a 45°, pistas más anchas para la alimentación y para la red que nombres (`net_widths`, p. ej. 2 mm para un calefactor), plano de masa en la cara de abajo |
| **Comprobación** | El DRC de KiCad con paridad con el esquema. Una placa que no implementa su esquema o incumple una regla nunca se entrega así: el ruteo se descarta y la placa vuelve sin rutear, con el motivo |

`export_fabrication` genera los Gerbers, los taladros, el fichero de posiciones para montaje, la lista de materiales y un zip listo para el fabricante, y se niega a hacerlo mientras la placa no pase esas comprobaciones.

### Qué no hace

- **No puede comprobar las piezas que tienes en la mano.** Patillajes y footprints salen de las librerías de KiCad y de las hojas de datos; un transistor cuyo fabricante ordena las patas de otra forma, o un módulo cuyo conector no es el de la librería, te toca comprobarlo antes de pedir placas.
- **No se inventa componentes.** Usa las librerías de símbolos instaladas de KiCad, y `find_part` / `import_part` traen lo que falte desde librerías KiCad externas, verificándolo antes de instalarlo. Si una pieza no existe en ninguna fuente, lo dice en vez de dibujarla.
- **No es un simulador.** Dibuja lo que le describes; no te dice si tu circuito es buena idea.

---

## Requisitos

| | |
|---|---|
| **KiCad 10** | Aporta `kicad-cli`, que se usa para el ERC y para renderizar. Versiones anteriores no están probadas. |
| **Claude Desktop** o **Claude Code** | O cualquier otro cliente MCP. |
| **Go 1.24+** | Solo si compilas desde el código fuente. |

---

## Instalación

### Opción A — un clic (recomendado)

Descarga **`mcp-kicad.mcpb`** desde [**Releases**](https://github.com/unmateria/MCP-Kicad/releases/latest) y haz doble clic. Claude Desktop lo instala como extensión: sin editar JSON y sin escribir rutas. El bundle lleva dentro las versiones de Windows, macOS y Linux, así que el mismo fichero vale para todo.

Después salta directamente a [Comprobar que funciona](#comprobar-que-funciona).

> En Apple Silicon la versión de macOS del bundle corre bajo Rosetta. Si prefieres la nativa, coge `mcp-kicad-darwin-arm64` de la Opción B.

### Opción B — descargar un binario

1. Entra en [**Releases**](https://github.com/unmateria/MCP-Kicad/releases) y descarga el fichero de tu sistema:

   | Sistema | Fichero |
   |---|---|
   | Windows (Intel/AMD) | `mcp-kicad-windows-amd64.exe` |
   | Linux (Intel/AMD) | `mcp-kicad-linux-amd64` |
   | Linux (ARM, p. ej. Raspberry Pi) | `mcp-kicad-linux-arm64` |
   | macOS (Apple Silicon) | `mcp-kicad-darwin-arm64` |
   | macOS (Intel) | `mcp-kicad-darwin-amd64` |

2. Ponlo donde quieras — por ejemplo `C:\Tools\mcp-kicad.exe` o `~/bin/mcp-kicad`.

3. En Linux y macOS, dale permiso de ejecución:

   ```bash
   chmod +x ~/bin/mcp-kicad
   ```

   En macOS el primer arranque queda bloqueado porque el binario no está firmado. Se desbloquea con:

   ```bash
   xattr -d com.apple.quarantine ~/bin/mcp-kicad
   ```

No hay que instalar nada más. El binario es autocontenido y no necesita fichero de configuración: encuentra KiCad por su cuenta.

### Opción C — compilar desde el código

```bash
git clone https://github.com/unmateria/MCP-Kicad.git
cd MCP-Kicad
go build -o mcp-kicad ./cmd/server      # añade .exe en Windows
```

---

## Conectarlo con Claude

*No hace falta si instalaste el bundle `.mcpb`: se registra solo.*

### Claude Desktop

Edita el fichero de configuración — créalo si no existe:

| Sistema | Ubicación |
|---|---|
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

Añade el servidor:

```json
{
  "mcpServers": {
    "kicad": {
      "command": "C:\\Tools\\mcp-kicad.exe",
      "args": []
    }
  }
}
```

En Linux o macOS el comando es una ruta normal: `"/home/tuusuario/bin/mcp-kicad"`.

> **Aviso para Windows:** en JSON las barras invertidas van dobladas (`C:\\Tools\\...`). Una sola barra es, con diferencia, el motivo más habitual de que el servidor no arranque.

**Después cierra Claude Desktop del todo y vuelve a abrirlo.** Recargar la ventana no basta: el servidor corre como proceso hijo y solo arranca en un reinicio completo.

### Claude Code

```bash
claude mcp add kicad -- /ruta/a/mcp-kicad
```

### Comprobar que funciona

Pídele a Claude:

> Usa get_project_info para comprobar la configuración de KiCad.

Debería devolverte la ruta de `kicad-cli` detectada, los directorios de librerías y el directorio de salida. Si dice que no encuentra `kicad-cli`, mira [Configuración](#configuración) más abajo.

---

## Cómo usarlo

Describe el circuito y ya está. Concreta lo que te importe — tensión de alimentación, referencias que quieras usar, valores que ya tengas decididos — y deja el resto en manos de Claude.

Peticiones que funcionan bien:

> Diséñame una fuente regulada de 5 V a partir de 12 V de entrada con un LM7805, con desacoplo a la entrada y a la salida y un LED de encendido.

> Hazme una placa mínima con un ATmega328P: cristal de 16 MHz con sus condensadores de carga, resistencia de pull-up en el reset, conector ICSP y desacoplo en los dos pines de alimentación.

> Monta un multivibrador astable de dos transistores que haga parpadear dos LEDs a unos 2 Hz.

Cosas útiles que pedir después:

- *"Enséñame el esquema"* — genera una imagen de vista previa.
- *"Expórtalo a PDF"* — a través de `kicad-cli`.
- *"Pasa el ERC"* — el chequeo eléctrico de KiCad, con las violaciones explicadas.
- *"Acerca los condensadores de desacoplo a U1 y recompila"* — la fuente del diseño es texto, así que revisar sale barato.

Los ficheros generados van al directorio de salida (`get_output_dir` te dice cuál es; `set_output_dir` lo cambia).

### La fuente del diseño

Por debajo, Claude escribe un documento JSON pequeño y lo compila. Rara vez tendrás que tocarlo, pero merece la pena verlo, porque explica por qué los resultados son estables: **las posiciones nunca se dan en milímetros, se anclan de pin a pin**.

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

<p align="center">
  <img src="docs/images/led_18650.svg" alt="El esquema compilado a partir de la fuente de arriba" width="260">
</p>

El primer símbolo de cada bloque lo ancla; los demás cuelgan de un pin de otro ya colocado, a un número entero de celdas de 2,54 mm. Así el esquema se mantiene en rejilla y legible por mucho que el modelo reordene las cosas.

El formato completo está especificado en [`internal/tools/design_format.md`](internal/tools/design_format.md) (el mismo texto que la herramienta `design_guide` le sirve al modelo), y hay trece ejemplos resueltos en [`docs/compiler/`](docs/compiler/) — desde un circuito de dos componentes hasta un controlador de invernadero con 27.

---

## Configuración

No hace falta ninguna. El servidor busca KiCad en los sitios habituales:

- **Windows** — `C:\Program Files\KiCad\<versión>\bin\kicad-cli.exe`, y después el `PATH`
- **Linux** — `/usr/bin/kicad-cli`, `/usr/local/bin/kicad-cli`, y después el `PATH`
- **macOS** — `/Applications/KiCad/KiCad.app/Contents/MacOS/kicad-cli`, y después el `PATH`

Si tienes la instalación en un sitio raro, o quieres cambiar dónde se escriben los ficheros, copia `config.ini.example` a `config.ini` **junto al ejecutable** y rellena lo que necesites:

```ini
[paths]
kicad_cli  = /opt/kicad/bin/kicad-cli
output_dir = /home/tuusuario/esquemas
libs_root  = /home/tuusuario/mcp-kicad-libs

[api_keys]
mouser                =
digikey_client_id     =
digikey_client_secret =
```

Las claves de API son opcionales y sólo aportan metadatos: ningún distribuidor
sirve ficheros CAD. Las fuentes que sí traen símbolos y footprints no necesitan
ninguna clave.

Por defecto, los ficheros generados van a `<tu carpeta personal>/mcp-kicad/output`.

---

## Herramientas

Expone treinta y cinco herramientas. En la práctica Claude lo hace casi todo a través de `compile_schematic`, `compile_pcb` y `export_fabrication`; el resto están para inspeccionar y para reparar un fichero ya existente.

**Diseñar**
`compile_schematic` · `design_guide` · `get_design_context` · `kicad_workflow_help` · `apply_template` · `list_templates`

**Buscar componentes**
`find_part` · `import_part` · `check_component_existence` · `symbol_pins` · `list_symbol_libraries` · `register_library`

**Leer un esquema**
`read_schematic` · `get_connectivity_summary` · `cluster_components` · `layout_metrics`

**Editar a mano**
`create_schematic` · `add_symbol` · `connect_pins` · `disconnect_pin` · `add_wire` · `add_label` · `add_power_rail` · `junction` · `no_connect` · `connect_netlist` · `batch_schematic`

**Placas**
`compile_pcb` · `export_fabrication` · `modify_pcb_layout`

**Comprobar y exportar**
`validate_design` (ERC/DRC) · `export_schematic_image` (SVG/PDF)

**Puesta a punto**
`get_project_info` · `get_output_dir` · `set_output_dir`

---

## Componentes que KiCad no trae

KiCad viene con unos 22 700 símbolos. El que necesitas muchas veces no está.
`find_part` busca en todas las fuentes a la vez e `import_part` instala un
candidato:

```
find_part   query="ESP32-C3-MINI-1"
import_part ref="espressif:symbols/Espressif.kicad_sym#ESP32-C3-MINI-1"
→ MCP_Imported:ESP32-C3-MINI-1   53 pines, footprint emparejado, listo para compile_schematic
```

| Fuente | Qué trae | Licencia |
|---|---|---|
| *installed* | las librerías de KiCad de esta máquina y todo lo ya importado | — |
| **jlcpcb** | el catálogo de montaje de JLCPCB: símbolo + footprint + modelo 3D, emparejados | MIT |
| **cern** | CERN Open Hardware: símbolos para miles de referencias reales | CERN-OHL-P-2.0 |
| **digikey-lib** | la librería de Digi-Key, 150 librerías por categoría | según el repositorio |
| **espressif** | la librería oficial de Espressif para ESP32 | según el repositorio |
| **sparkfun** | placas, sensores y conectores de SparkFun | CC-SA-4.0 |
| **lcsc** | LCSC / EasyEDA convertido a KiCad — la cola larga, por número C | datos de terceros |
| **mouser**, **digikey** | sólo identificación: el MPN real, el fabricante, el encapsulado y el datasheet detrás de un código de pedido. **Sin ficheros CAD** — ningún distribuidor los sirve. Requiere clave de API | — |

Cada fuente se indexa una vez en `libs/cache/` y a partir de ahí se busca sin
red. No se instala nada hasta haberlo verificado:

1. parsea,
2. `kicad-cli` lo relee y lo reescribe,
3. se coloca en un esquema de prueba con sus pines resueltos,
4. sus pines se comparan con los pads del footprint,
5. KiCad lo dibuja y te devuelve la imagen.

**Lo que falla no se instala.** Un símbolo importado a medias es peor que
ningún símbolo, porque parece que funciona. Y si una pieza no existe en ninguna
fuente, la respuesta es que no existe: nunca un sustituto elegido en silencio,
nunca geometría dibujada de memoria.

Todo va a una sola librería, `MCP_Imported`, registrada en KiCad para que el
selector de símbolos del GUI también la vea. Cada símbolo importado lleva una
propiedad `MCP_Source` con su procedencia, su licencia y la fecha — por eso
`libs/` no se versiona: es reproducible desde las fuentes.

---

## Cómo funciona por dentro

`.design.json` → **compilar** → `.kicad_sch`

1. **Colocar.** La posición de cada símbolo sale de un anclaje a un pin, resuelto a coordenadas absolutas sobre la rejilla de 2,54 mm.
2. **Cablear.** Las conexiones cortas y evidentemente correctas se trazan con geometría cerrada; las largas pasan por un router A\* que esquiva los cuerpos de los componentes.
3. **Alimentación.** Un símbolo de alimentación por pin, desplazado en la dirección a la que apunta el pin, y luego alineados formando raíles.
4. **Barrera.** Se revisa cada cable. Todo lo que cruce otra net, corte un símbolo o se solape en colineal se borra y se sustituye por etiquetas de net: la conectividad se conserva, la mentira no.
5. **Rematar.** Los textos de referencia y valor se apartan de cuerpos y cables; la hoja se centra y se sube de tamaño de papel si el circuito no cabe.
6. **Verificar.** El fichero se vuelve a leer desde cero, se traza su netlist y se compara con la fuente, y `kicad-cli` ejecuta el ERC.

`.kicad_sch` → **compile_pcb** → `.kicad_pcb`

1. **Netlist.** La exporta `kicad-cli`: los mismos datos que usa *Actualizar PCB desde esquemático*.
2. **Colocar.** Las posiciones del esquema sirven de semilla a varias escalas; las piezas se legalizan sin solapes, se giran para acortar conexiones, los condensadores de desacoplo van a la patilla de alimentación de su chip y los conectores al borde.
3. **Rutear.** Un enrutador de rejilla de dos caras escrito para este proyecto a partir de algoritmos publicados (A\* con coste de curvas, negociación PathFinder y una pasada de mejora), sin código de otros enrutadores. Su cobre se mide al milímetro contra el de cada red antes de entregarlo.
4. **Verificar.** KiCad reescribe el fichero, rellena el plano de masa y pasa el DRC con paridad con el esquema. Una variante que falla se sustituye por la siguiente mejor: plano con alivio térmico, plano con unión sólida, sin plano, sin rutear.

Los ficheros de KiCad se leen y se escriben con un parser de S-expresiones de verdad ([`internal/sexp`](internal/sexp)), nunca buscando patrones en el texto.

---

## Limitaciones conocidas

- **Los textos todavía pueden solaparse en hojas densas.** Las referencias y las etiquetas de net se llevan a la posición con menos solape disponible, pero en un esquema apretado la mejor posición posible a veces sigue tocando algo. El compilador informa exactamente de lo que queda y de cuánta separación extra haría falta para resolver cada caso.
- **Algunas conexiones acaban en etiqueta en vez de en cable.** Es la barrera haciendo su trabajo. Eléctricamente idéntico, estéticamente peor.
- **Las vistas previas en PNG necesitan un navegador de la familia Chromium** (Edge, Chrome, Chromium o Brave). Sin uno, se usa un renderizador en Go puro de menor calidad. Solo la ruta de Windows está verificada en la práctica.
- **Las placas son de dos caras y salen de un enrutador de rejilla.** Rutea todas las placas del corpus de referencia, a 45°; no sustituye al diseño a mano en alta velocidad, RF o alta tensión. Donde el plano de masa no puede dar dos radios térmicos a un pad, lo une en sólido y dice qué pad.
- **La colocación parte del esquema.** Un esquema ordenado da una placa ordenada; lo que el esquema pone lejos empieza lejos.
- **Probado con Claude Desktop y Claude Code.** Otros clientes MCP deberían funcionar, pero no se han probado.

---

## Desarrollo

```bash
go build -o mcp-kicad ./cmd/server   # compilar
go test ./...                        # todos los tests
go run ./cmd/verify_e2e              # prueba de humo de extremo a extremo
go run ./cmd/compile -o out.kicad_sch docs/compiler/led_18650.design.json
go run ./cmd/measure_layout out.kicad_sch    # métricas de calidad del layout
go run ./cmd/pininfo <libreria.kicad_sym>    # posiciones de pines de una librería
go run ./cmd/compile_pcb -widths "+12V=1" out.kicad_sch   # colocar y rutear una placa
go run ./cmd/export_fab out.kicad_pcb        # Gerbers, taladros, BOM, posiciones

# Las fuentes de componentes son afirmaciones sobre el mundo exterior.
# `go test ./...` nunca toca la red; esto las vuelve a medir bajo demanda.
MCP_KICAD_LIVE=1 go test ./internal/parts/providers/ -run TestLive -v
MCP_KICAD_LIVE=1 go test ./internal/tools/ -run TestLiveImportAndCompile -v
```

Las trece fuentes de `docs/compiler/` son el corpus de referencia: cada cambio en el pipeline se comprueba contra todas ellas, como esquemas y como placas.

---

## Licencia

[PolyForm Noncommercial License 1.0.0](LICENSE.md) — libre para usar, modificar y compartir con cualquier fin **no comercial**, incluyendo proyectos personales, electrónica como afición, docencia, investigación y organizaciones sin ánimo de lucro. El uso comercial no está concedido por esta licencia.
