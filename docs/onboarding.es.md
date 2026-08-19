# Onboarding — Cloud Native Ayacucho Community (CNAYP) · español

Texto fuente de lo que Discord muestra a quien entra al servidor: la **Server Guide** (cartel de
bienvenida, tareas para nuevos miembros y recursos) y el **Rules Screening** (las reglas que hay que
aceptar antes de poder escribir). Nada de esto sustituye a las reglas completas de
[`community-rules.es.md`](community-rules.es.md) —que siguen siendo las que rigen— ni al resumen de
un mensaje de [`community-rules-short.es.md`](community-rules-short.es.md); solo apunta hacia ellas.

Solo existe en español, sin gemelo en inglés como el resto de `docs/`, porque Discord muestra **un**
cartel de bienvenida y **un** conjunto de reglas por servidor: no hay dónde poner una segunda lengua.
La regla 4 ya dice que el inglés es bienvenido y el resumen enlaza la versión en inglés.

Este archivo es la fuente de verdad: edita aquí y vuelve a pegar en Discord. Si cambias una regla en
la versión completa, revisa que las reglas condensadas de abajo la sigan contando bien.

**Dónde va cada cosa.** Todo se configura a mano en el cliente de Discord; el bot no interviene.

| Sección | Pantalla | Límite |
| --- | --- | --- |
| Cartel de bienvenida | Ajustes del servidor → Comunidad → **Onboarding** → Server Guide → *Welcome Sign* | ~300 caracteres, sin markdown |
| Tareas para nuevos miembros | misma pantalla → *New Member To Do's* | cada tarea enlaza a un canal |
| Recursos | misma pantalla → *Resources* | enlaces fijos |
| Reglas condensadas | Ajustes del servidor → Moderación → **Safety Setup** → *Rules Screening* → Enable → Edit rules | ~300 caracteres por regla, texto plano, una lengua |

---

## Cartel de bienvenida (Welcome Sign)

**Message Author**: `kenesparta` (o el moderador que quieras que firme). El mensaje sale con su avatar,
así que va en primera persona del plural, no institucional. `[@username]` lo sustituye Discord por la
mención de cada persona; déjalo tal cual.

285 caracteres:

```
¡Hola [@username]! Te damos la bienvenida a la Cloud Native Ayacucho Community (CNAYP). Somos voluntarios que aprendemos y compartimos conocimiento sobre cloud native. Aquí no hay preguntas tontas: pregunta con contexto y comparte lo que aprendas. Empieza por las reglas y preséntate 👇
```

Si el cuadro se queja de largo, la versión corta (178 caracteres):

```
¡Hola [@username]! Bienvenido, bienvenida a CNAYP, la comunidad cloud native de Ayacucho. Venimos a aprender y a compartir: no hay preguntas tontas. Lee las reglas y preséntate 👇
```

El «👇» apunta a las tareas de abajo, que salen en la misma pantalla. Sin ellas el mensaje queda cojo.

## Tareas para nuevos miembros (New Member To Do's)

Una tarea por línea; cada una enlaza a un canal. Ajusta los nombres de canal a los que existan.

| Emoji | Título | Canal | Descripción |
| --- | --- | --- | --- |
| 📜 | Lee las reglas | `#reglas` | Un minuto. Cortas y sin sorpresas. |
| 👋 | Preséntate | `#presentaciones` | Quién eres y qué quieres aprender. |
| 💬 | Saluda en general | `#general` | O lanza tu primera pregunta. |

En `#reglas`, el primer mensaje que se ve al llegar desde aquí conviene que sea el resumen de un
mensaje de [`community-rules-short.es.md`](community-rules-short.es.md), seguido de los cuatro
mensajes completos.

## Recursos (Resources)

| Emoji | Título | Enlace |
| --- | --- | --- |
| 📖 | Reglas completas | `#reglas` |
| 🧭 | Código de Conducta de la CNCF | https://github.com/cncf/foundation/blob/main/code-of-conduct.md |

## Reglas condensadas (Rules Screening)

Cada línea numerada es **una regla aparte** en el formulario; pégalas sin el número. Es texto plano:
no se renderiza markdown ni se convierten las menciones de canal, así que `#reglas` queda como texto
y sirve solo de pista. Añade también la descripción del servidor que pide el formulario; vale la
primera frase del cartel de bienvenida.

```
1. Respeto ante todo: empatía, amabilidad y lenguaje inclusivo. Nada de insultos, acoso, ataques personales o políticos ni contenido sexualizado. Aplica el Código de Conducta de la CNCF v1.3.
2. Este es un espacio para aprender: no hay preguntas tontas. Pregunta directo y con contexto (qué haces, versión, error completo, qué probaste) y comparte la solución cuando la encuentres. Nadie está obligado a responder.
3. Usa el canal correcto y los hilos para conversaciones largas. Sin flood, mayúsculas sostenidas, mensajes repetidos ni @everyone/@here.
4. Español e inglés son bienvenidos. Escribe en el idioma en que te sientas cómodo o cómoda.
5. Nada de spam ni autopromoción: sin DMs masivos, referidos ni invitaciones a otros servidores sin permiso. Comparte tu blog, meetup o vacante una vez en el canal que corresponde y quédate a conversar.
6. Prohibidos los dumps de examen (CKA, CKAD, CKS, KCNA…), el software pirata y las cuentas compartidas de plataformas de pago.
7. Cuida la privacidad: no publiques datos personales ajenos ni capturas privadas. Revisa lo que pegas (tokens, kubeconfig, claves, IP internas). Si filtras una credencial, rótala y avisa a un moderador.
8. Nada de estafas: cripto, airdrops, NFT, señales de trading, apuestas ni «oportunidades» de inversión. Ningún moderador te pedirá contraseñas, tokens ni pagos por DM.
9. Cumple los Términos y las Normas de Discord: mínimo 13 años, sin NSFW, contenido ilegal o de odio; una cuenta por persona.
10. ¿Algo te incomoda? Escribe por DM a cualquier persona con el rol Moderador o a kenesparta@pm.me. Los reportes son confidenciales y sin represalias. Reglas completas en #reglas · Full rules in English in #rules.
```
