# The frontend image.
#
#   docker build -t lgtm/frontend:v1 --file frontend/Dockerfile .
#
# The context is the repository root, like the backend's, so that one rule
# builds every image.

FROM nginx:1.30.4-alpine


COPY frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY frontend/index.html frontend/app.js frontend/styles.css frontend/example.rs \
     /usr/share/nginx/html/
COPY frontend/vendor/ /usr/share/nginx/html/vendor/

RUN find /usr/share/nginx/html -type f \
      \( -name '*.js' -o -name '*.css' -o -name '*.html' \
         -o -name '*.ttf' -o -name '*.rs' \) \
      -exec gzip -9 -k {} + \
 && nginx -t

EXPOSE 80
