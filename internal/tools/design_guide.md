# Guía de diseño para autores de `.design.json`

Cómo piensa un ingeniero humano al dibujar un esquemático, traducido a la
fuente declarativa de este compilador. El ejemplo y la spec del formato van
JUSTO ARRIBA, en esta misma respuesta; esta parte es el criterio con el que
usarlos.
Fuentes: SparkFun, KiCad KLC, IEEE 315, Altium, Horowitz & Hill, y las
lecciones de los siete circuitos canónicos del repo.

## El reparto de papeles

**Tú decides** (y eres responsable de que parezca humano): la topología en
bloques, qué componente ancla a cuál, distancias en celdas, orientaciones,
el ORDEN de los pines en cada net (define la cadena de cableado).

**La máquina garantiza** (no luches contra ella): rejilla de 2.54, cero
solapes de cuerpos, cero cruces entre nets, cable solo donde la geometría es
limpia (recto o pocas esquinas; las serpientes se demotan a etiqueta), un
símbolo power por pin, PWR_FLAG automático con el texto oculto, campos de
texto recolocados sin colisiones, pares de etiquetas cercanos soldados a
cable cuando existe corredor limpio.

## Disposición (lo que más se nota)

1. **La señal fluye de izquierda a derecha**: entradas/fuentes a la
   izquierda, cargas/salidas a la derecha. La realimentación vuelve por la
   derecha (o por etiqueta si el lazo sería una serpiente).
2. **Alimentación arriba, GND abajo.** Los condensadores verticales con el
   pin 1 arriba hacia el rail y el pin 2 abajo hacia GND caen así solos.
3. **Un bloque = una idea funcional** (alimentación, MCU, oscilador, bus,
   salida). Usa bloques del formato para eso; `arrange` en filas cuenta la
   historia del circuito de arriba a abajo.
4. **El desacoplo vive pegado a su IC** — granja en fila junto al pin de
   alimentación, condensadores separados 4 celdas (a 3 el texto de valores
   se toca). El bulk (10u) al extremo de la granja.
5. **Cristales y cargas junto a sus pines XTAL**: cristal a 5 celdas del
   pin, condensadores de carga colgando 2-3 celdas hacia GND.
6. **Alineación o muerte**: ancla en cadena (C2 respecto a C1, C3 respecto
   a C2...) para que las filas queden perfectas por construcción.
7. **Densidad uniforme**: si un bloque queda apretado y otro vacío, reparte
   celdas — el compilador rechaza solapes, pero el "aire" lo decides tú.

## Cables y etiquetas (la firma humana)

8. **Cable si la ruta es digna: recta o una L.** El router lo intenta; si
   la ruta sale con más de 3 codos o un desvío >70%, la máquina la demota a
   etiqueta — eso es un síntoma de que los dos extremos están mal colocados:
   acércalos o alinéalos si esa conexión merece cable.
9. **Alinear pines que se conectan**: dos pines a la misma altura (misma Y)
   producen el cable recto perfecto. La forma más barata de lograrlo es
   anclar uno al otro con `dir` + `cells`.
9b. **Respeta la dirección de salida del pin**: un pin que apunta hacia
   arriba (el + de una batería, un VCC) quiere que lo siguiente esté
   ARRIBA. Anclar el siguiente componente a su misma altura produce una
   joroba (sube-cruza-baja); ancla en la dirección del pin (1-2 celdas) y
   el cable queda un palito recto. Recoloca el componente, no dobles el
   cable.
10. **Etiqueta para lo lejano**, siempre con nombre semántico (`SDA`,
    `RESET_N`, `VOUT`) — jamás rellenos tipo `NET1`. Mayúsculas
    consistentes; activo-bajo con sufijo `_N`.
10b. **Los nudos internos no llevan tag**: el punto entre una resistencia y
    su LED no se etiqueta en un esquema a mano. Nómbralos con prefijo `_`
    (`_ANODE`) — conectan igual pero no imprimen etiqueta si el cable ya
    une todo.
11. **Los rails no se cablean**: cada pin de un power net recibe su símbolo
    (política per-pin). GND repetido muchas veces es lo profesional, no un
    defecto.
12. **Nada de cruces en "+"**: la maquinaria ya los impide; si ves que una
    conexión insiste en cruzar, mueve el componente, no aceptes la etiqueta
    como resignación.

## Texto (menos es más)

13. En pantalla solo referencia + valor (R/C/L) o referencia + modelo (ICs,
    diodos especiales). El compilador ya oculta el resto (PWR_FLAG, campos
    técnicos); no añadas ruido tú.
14. El texto siempre horizontal — lo garantiza la pasada de texto — pero tú
    decides el AIRE: si un valor queda incrustado entre símbolos, es que
    faltan celdas entre ellos.

## Números que funcionan (ganados a base de PDFs)

| Situación | Receta |
|---|---|
| Granja de desacoplo | primera C a 8 celdas del pin VCC del IC (los cuerpos de MCU tienen media anchura ~5 celdas); resto encadenadas a **5** — a 4 el bloque `ref+valor` de cada C aterriza sobre el cuerpo de su vecina (medido: 4.55 mm² de solape por par) |
| Cristal + cargas | cristal a 5 celdas del pin XTAL; cargas 2-3 celdas hacia abajo |
| Pasivo junto a IC pequeño (555, regulador) | 6 celdas del pin (a 4, el texto del pasivo pisa los nombres de pin del IC) |
| Cadena serie (R→LED) | eslabones a 3-4 celdas, misma Y, rot 90 para que quede horizontal |
| Bloques en arrange | deja que el margen automático (4 celdas) trabaje; no compenses a mano |

## Protocolo de iteración

1. Compila. 2. Mira el PNG y el informe (el gate te dice QUÉ demotó y POR
QUÉ; el solape te da las dos refs implicadas). 3. Toca los números de la
fuente — nunca el `.kicad_sch`. 4. Recompila. Dos o tres vueltas bastan si
las recetas de arriba son tu punto de partida.

El informe trae dos líneas que no hay que leer con el ojo, sino obedecer:

- `netlist:` — tiene que decir *verified*. Si dice **FAILED**, el esquema
  NO implementa la netlist que declaraste (un cable acabó en un pin ajeno,
  o un net se partió): es un error de verdad, no algo cosmético.
- `text:` — colisiones de texto que quedan. Cero es alcanzable: cinco de
  los siete circuitos de referencia están a cero. Si aparece un número,
  casi siempre sobran celdas entre dos símbolos.

## Delatores de "esquema hecho por ordenador" (y su antídoto)

- Componentes sin agrupar → bloques funcionales + `arrange`.
- Par de etiquetas a 2 cm que pudo ser un cable → acerca/alinea los pines
  (la soldadora convierte el par en cable en cuanto el corredor está limpio).
- Texto pisado o vertical → más celdas entre símbolos.
- Desacoplo lejos de su IC → ancla la granja al pin, no al bloque.
- Serpientes de cable rodeando símbolos → etiqueta o recolocación.
