# Formato `.design.json`

La especificación normativa vive en `internal/tools/design_format.md`, porque
está **embebida en el binario** y la sirve la herramienta MCP `design_guide`.

Esa es la razón del traslado: un LLM que use el servidor desde un cliente sin
sistema de ficheros (Claude Desktop, por ejemplo) no puede leer este
directorio, y sin la sintaxis a mano acaba deduciéndola a base de mensajes de
rechazo — que es exactamente lo que pasó en la primera sesión real de uso.

Para leerla: `internal/tools/design_format.md`, o llamar a `design_guide`.
