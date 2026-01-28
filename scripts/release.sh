#!/bin/bash

# Функция для инкремента версии (patch)
increment_version() {
    local version=$1
    local delimiter="."
    local array=(${version//$delimiter/ })
    array[2]=$((array[2] + 1))
    echo "${array[0]}.${array[1]}.${array[2]}"
}

# Получаем текущую версию из последнего тега
CURRENT_TAG=$(git describe --tags --abbrev=0 2>/dev/null)
if [ -z "$CURRENT_TAG" ]; then
    # Если тегов нет, берем версию из Chart.yaml или ставим начальную
    CURRENT_VERSION=$(grep "^version:" deploy/charts/metric-cacher-exporter-helm/Chart.yaml | awk '{print $2}')
    [ -z "$CURRENT_VERSION" ] && CURRENT_VERSION="0.0.0"
else
    CURRENT_VERSION=${CURRENT_TAG#v}
fi

NEW_VERSION=$(increment_version $CURRENT_VERSION)
NEW_TAG="$NEW_VERSION"

echo "Текущая версия: $CURRENT_VERSION"
echo "Новая версия: $NEW_VERSION ($NEW_TAG)"

# 1. Обновляем Chart.yaml через существующий скрипт (он возьмет тег, если мы его уже создадим, 
# либо мы можем передать версию напрямую. Но лучше сначала обновить файлы, потом тегировать)

CHART_FILE="deploy/charts/metric-cacher-exporter-helm/Chart.yaml"
sed -i.bak -E "s/^version: .*/version: $NEW_VERSION/" "$CHART_FILE"
sed -i.bak -E "s/^appVersion: .*/appVersion: \"$NEW_VERSION\"/" "$CHART_FILE"
rm -f "${CHART_FILE}.bak"

# 2. Добавляем изменения в git
git add "$CHART_FILE"

# 3. Делаем коммит релиза
git commit -m "release: $NEW_VERSION"

# 4. Создаем тег
git tag -a "$NEW_TAG" -m "Release $NEW_TAG"

echo "Релиз $NEW_TAG локально создан."
echo "Проверьте изменения и выполните push вручную (или раскомментируйте в скрипте):"
echo "# git push origin main"
echo "# git push origin $NEW_TAG"

# git push origin main
# git push origin $NEW_TAG
