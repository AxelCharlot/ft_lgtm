# The frontend image.
#

FROM nginx:1.30.4-alpine


COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html app.js styles.css example.rs /usr/share/nginx/html/
COPY vendor/ /usr/share/nginx/html/vendor/

RUN find /usr/share/nginx/html -type f \
      \( -name '*.js' -o -name '*.css' -o -name '*.html' \
         -o -name '*.ttf' -o -name '*.rs' \) \
      -exec gzip -9 -k {} + \
 && nginx -t

EXPOSE 80
