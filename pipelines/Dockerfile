# Airflow with a local Spark (spark-submit --master local[*]) and the Trino
# client, so the whole nightly pipeline runs in one container.
FROM apache/airflow:2.10.5-python3.11

USER root
# A JRE for Spark, copied from the official Temurin image (no apt needed),
# and the S3A connector jars matching Spark 3.5's Hadoop 3.3.4.
COPY --from=eclipse-temurin:17-jre /opt/java/openjdk /opt/java/openjdk
RUN mkdir -p /opt/spark-jars \
 && curl -fsSL --retry 5 -o /opt/spark-jars/hadoop-aws-3.3.4.jar \
      https://repo1.maven.org/maven2/org/apache/hadoop/hadoop-aws/3.3.4/hadoop-aws-3.3.4.jar \
 && curl -fsSL --retry 5 -o /opt/spark-jars/aws-java-sdk-bundle-1.12.262.jar \
      https://repo1.maven.org/maven2/com/amazonaws/aws-java-sdk-bundle/1.12.262/aws-java-sdk-bundle-1.12.262.jar
ENV JAVA_HOME=/opt/java/openjdk \
    PATH=/opt/java/openjdk/bin:$PATH \
    SPARK_EXTRA_JARS=/opt/spark-jars/hadoop-aws-3.3.4.jar,/opt/spark-jars/aws-java-sdk-bundle-1.12.262.jar \
    PYTHONPATH=/opt/lumen/pipelines \
    AIRFLOW__CORE__DAGS_FOLDER=/opt/lumen/pipelines/dags \
    AIRFLOW__CORE__LOAD_EXAMPLES=False

USER airflow
# Airflow's constraints file pins pyspark (3.5.x, matching hadoop-aws 3.3.4)
# and the trino client to versions tested with this Airflow release.
RUN pip install --no-cache-dir "apache-airflow==2.10.5" pyspark trino \
      --constraint "https://raw.githubusercontent.com/apache/airflow/constraints-2.10.5/constraints-3.11.txt"

COPY --chown=airflow:root . /opt/lumen/pipelines
COPY --chown=airflow:root entrypoint.sh /opt/lumen/entrypoint.sh
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/opt/lumen/entrypoint.sh"]
