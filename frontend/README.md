# TREMOR СКЛАД — frontend

React + TypeScript + Vite. Запускать команды из папки `frontend`:

```powershell
npm ci
npm run dev
npm test
npm run build
npm run lint
```

Локальная страница: `http://127.0.0.1:5173`. Адрес API задан в `src/config/api.ts`.

Общие элементы сайта:

- Шапка: `src/components/Header`.
- Переходы между страницами: `src/components/PageIntro`, подключены в `src/App.tsx`.
- Масштаб и фон обеих тем: `src/index.css`; переключение темы: `src/hooks/useTheme.ts`.

Не дублируйте эти настройки в стилях новых страниц. `npm test` запускает все локальные проверки `scripts/test-*.mjs` и останавливается при первой ошибке. Сборка дополнительно проверяет TypeScript, линтер запускается отдельной командой.

Проверку серверных сценариев и настройку отдельной тестовой базы смотрите в основном README проекта. `npm audit` проверяет известные предупреждения зависимостей по реестру npm; успешный audit не заменяет тесты приложения.
