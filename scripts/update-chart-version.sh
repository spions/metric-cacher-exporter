#!/bin/bash

# Путь к Chart.yaml
CHART_FILE="deploy/charts/metric-cacher-exporter-helm/Chart.yaml"

# Пытаемся получить версию из последнего тега Git
# Если тегов нет, можно задать начальную версию
VERSION=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')

if [ -z "$VERSION" ]; then
    echo "Теги не найдены. Используется текущая версия из Chart.yaml"
    exit 0
fi

echo "Обновление Chart.yaml версией из тега: $VERSION"

# Обновляем version и appVersion в Chart.yaml
# Используем sed для замены значений
sed -i.bak -E "s/^version: .*/version: $VERSION/" "$CHART_FILE"
sed -i.bak -E "s/^appVersion: .*/appVersion: \"$VERSION\"/" "$CHART_FILE"

# Удаляем временный файл бэкапа (создается sed на macOS)
rm -f "${CHART_FILE}.bak"

# Добавляем файл в индекс, чтобы изменения попали в коммит
git add "$CHART_FILE"
