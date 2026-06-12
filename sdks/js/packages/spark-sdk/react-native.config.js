export default {
  dependency: {
    platforms: {
      android: {
        packageImportPath:
          "import com.spark.SparkPackage;\nimport com.sparktokenprimitives.SparkTokenPrimitivesPackage;",
        packageInstance:
          "new SparkPackage(), new SparkTokenPrimitivesPackage()",
      },
    },
  },
};
