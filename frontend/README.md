# TREMOR СКЛАД — frontend

React + TypeScript + Vite. Запускать команды из папки `frontend`:

```powershell
npm install
npm run dev
npm run build
npm run lint
```

Локальная страница: `http://127.0.0.1:5173`. Адрес API задан в `src/config/api.ts`.

Общие элементы сайта:

- Шапка: `src/components/Header`.
- Переходы между страницами: `src/components/PageIntro`, подключены в `src/App.tsx`.
- Масштаб и фон обеих тем: `src/index.css`; переключение темы: `src/hooks/useTheme.ts`.

Не дублируйте эти настройки в стилях новых страниц. Проверку серверных сценариев и настройку отдельной тестовой базы смотрите в основном README проекта.
