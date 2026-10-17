FROM node:20-slim
WORKDIR /evals
COPY package.json ./
RUN npm install --no-audit --no-fund
COPY server.js ./
COPY public ./public
EXPOSE 8300
CMD ["node", "server.js"]
