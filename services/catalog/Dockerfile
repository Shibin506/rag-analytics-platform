FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml ./
RUN mvn -q -B dependency:go-offline
COPY src ./src
RUN mvn -q -B -DskipTests package && cp target/catalog-*.jar /app.jar

FROM eclipse-temurin:21-jre
RUN useradd -r -u 10001 lumen
COPY --from=build /app.jar /app.jar
USER lumen
EXPOSE 8081
HEALTHCHECK --interval=10s --timeout=3s --retries=18 \
  CMD bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081 && printf "GET /actuator/health HTTP/1.0\r\n\r\n" >&3 && grep -q UP <&3'
ENTRYPOINT ["java", "-XX:MaxRAMPercentage=75", "-jar", "/app.jar"]
